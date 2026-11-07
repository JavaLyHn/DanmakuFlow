package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

var serviceOrder = []string{"mysql", "redis", "kafka", "consumer", "server"}
var managedOrder = []string{"mysql", "redis", "kafka", "consumer", "server", "migrate", "kafka-init"}
var labels = map[string]string{"mysql": "MySQL", "redis": "Redis", "kafka": "Kafka", "consumer": "消息消费者", "server": "弹幕服务", "migrate": "数据库初始化", "kafka-init": "Kafka 初始化"}
var startLimits = map[string]time.Duration{"mysql": 30 * time.Second, "redis": 10 * time.Second, "kafka": 90 * time.Second, "consumer": 30 * time.Second, "server": 30 * time.Second}
var stopLimits = map[string]time.Duration{"mysql": 30 * time.Second, "redis": 15 * time.Second, "kafka": 60 * time.Second, "consumer": 60 * time.Second, "server": 30 * time.Second, "migrate": 30 * time.Second, "kafka-init": 30 * time.Second}

type Instance struct {
	Identity  localproc.Identity `json:"identity"`
	Container *ContainerIdentity `json:"container,omitempty"`
	Owned     bool               `json:"owned"`
	RunID     string             `json:"runId,omitempty"`
	RunDir    string             `json:"runDir,omitempty"`
	Ready     bool               `json:"ready"`
	Stopped   bool               `json:"stopped,omitempty"`
}
type ServiceView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Phase  string `json:"phase"`
	Owned  bool   `json:"owned"`
	Detail string `json:"detail,omitempty"`
}
type Snapshot struct {
	Busy               bool          `json:"busy"`
	Operation          string        `json:"operation"`
	Phase              string        `json:"phase"`
	Progress           string        `json:"progress"`
	Error              string        `json:"error,omitempty"`
	ConfigurationError string        `json:"configurationError,omitempty"`
	CanStart           bool          `json:"canStart"`
	CanStop            bool          `json:"canStop"`
	Services           []ServiceView `json:"services"`
	Port               int           `json:"port"`
}
type Driver interface {
	Validate() error
	Discover(context.Context, string, *Instance) (*Instance, error)
	Start(context.Context, string) (Instance, error)
	Probe(context.Context, string, Instance) error
	Alive(Instance) bool
	Stop(context.Context, string, Instance, map[int32]int64) error
	Targets(context.Context) (map[int32]int64, error)
}
type savedState struct {
	ConfigKey       string              `json:"configKey"`
	Stopping        bool                `json:"stopping"`
	Instances       map[string]Instance `json:"instances"`
	ConsumerTargets map[int32]int64     `json:"consumerTargets"`
}
type Manager struct {
	mu                                    sync.Mutex
	driver                                Driver
	stateFile, key                        string
	instances                             map[string]Instance
	consumerTargets                       map[int32]int64
	views                                 map[string]ServiceView
	busy, stopping                        bool
	generation                            uint64
	operation, phase, progress, lastError string
}

func NewManager(d Driver, stateFile, key string) (*Manager, error) {
	m := &Manager{driver: d, stateFile: stateFile, key: key, instances: map[string]Instance{}, views: map[string]ServiceView{}, phase: "idle", progress: "点击启动后端，准备本机互动环境。"}
	for _, id := range managedOrder {
		m.views[id] = ServiceView{ID: id, Label: labels[id], Phase: "stopped"}
	}
	if stateFile != "" {
		var saved savedState
		if err := runcontrol.ReadJSON(stateFile, &saved); err == nil {
			if saved.ConfigKey != key {
				for _, inst := range saved.Instances {
					if inst.Owned && !inst.Stopped {
						return nil, errors.New("本机配置已更改，请先按原配置停止已有实例；未认领旧进程")
					}
				}
				// A completed lifecycle has no owned process left to recover.
				// Recheck external services using the new configuration on start.
				return m, nil
			}
			m.stopping = saved.Stopping
			m.consumerTargets = saved.ConsumerTargets
			for _, id := range managedOrder {
				if inst, ok := saved.Instances[id]; ok {
					m.instances[id] = inst
					v := m.views[id]
					v.Owned = inst.Owned
					v.Phase = "unknown"
					v.Detail = "正在核对原进程身份"
					if inst.Stopped {
						v.Phase = "stopped"
						v.Detail = "已确认停止"
					}
					m.views[id] = v
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取本机进程记录失败: %w", err)
		}
	}
	return m, nil
}
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{Busy: m.busy, Operation: m.operation, Phase: m.phase, Progress: m.progress, Error: m.lastError, CanStart: !m.busy && !m.stopping, Port: 8080, Services: []ServiceView{}}
	if err := m.driver.Validate(); err != nil {
		s.ConfigurationError = err.Error()
		s.CanStart = false
	}
	for _, id := range managedOrder {
		if isSetupJob(id) {
			if _, exists := m.instances[id]; !exists {
				continue
			}
		}
		v := m.views[id]
		s.Services = append(s.Services, v)
		if v.Owned && v.Phase != "stopped" {
			s.CanStop = true
		}
	}
	s.CanStop = s.CanStop && !m.busy
	return s
}
func (m *Manager) saveLocked() error {
	if m.stateFile == "" {
		return nil
	}
	return runcontrol.AtomicJSON(m.stateFile, savedState{ConfigKey: m.key, Stopping: m.stopping, Instances: m.instances, ConsumerTargets: m.consumerTargets})
}
func (m *Manager) begin(op string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return errors.New("另一项启停操作正在进行")
	}
	if op == "start" && m.stopping {
		return errors.New("请先完成上一次停止操作")
	}
	if err := m.driver.Validate(); err != nil && op == "start" {
		m.lastError = err.Error()
		m.phase = "failed"
		return err
	}
	m.busy = true
	m.generation++
	m.operation = op
	m.lastError = ""
	m.phase = "starting"
	if op == "stop" {
		m.phase = "stopping"
		m.stopping = true
	}
	if err := m.saveLocked(); err != nil {
		m.busy = false
		m.lastError = err.Error()
		return err
	}
	return nil
}
func (m *Manager) Start() error {
	if err := m.begin("start"); err != nil {
		return err
	}
	go m.start()
	return nil
}
func (m *Manager) Stop() error {
	if err := m.begin("stop"); err != nil {
		return err
	}
	go m.stop()
	return nil
}
func (m *Manager) finish(err error, phase, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.busy = false
	m.operation = ""
	m.progress = message
	m.phase = phase
	if err != nil {
		m.lastError = err.Error()
	}
	if e := m.saveLocked(); e != nil {
		m.lastError = "保存进程记录失败: " + e.Error()
		m.phase = "failed"
	}
}
func (m *Manager) view(id, phase, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.views[id]
	v.Phase = phase
	v.Detail = detail
	m.views[id] = v
	m.progress = labels[id] + "：" + detail
}
func (m *Manager) record(id string, inst Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.instances[id] = inst
	v := m.views[id]
	v.Owned = inst.Owned
	m.views[id] = v
	return m.saveLocked()
}
func (m *Manager) instance(id string) (Instance, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.instances[id]
	return i, ok
}
func (m *Manager) start() {
	m.startServices(context.Background(), serviceOrder, startLimits)
}
func (m *Manager) startServices(parent context.Context, order []string, limits map[string]time.Duration) {
	if preflight, ok := m.driver.(interface {
		Preflight(context.Context, map[string]Instance) error
	}); ok {
		m.mu.Lock()
		known := make(map[string]Instance, len(m.instances))
		for k, v := range m.instances {
			known[k] = v
		}
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		err := preflight.Preflight(ctx, known)
		cancel()
		if err != nil {
			m.finish(err, "failed", "端口或已有业务进程冲突，未继续启动。")
			return
		}
	}
	for _, id := range order {
		m.view(id, "starting", "检查已有实例")
		ctx, cancel := context.WithTimeout(parent, limits[id])
		old, ok := m.instance(id)
		var previous *Instance
		if ok {
			previous = &old
		}
		inst, err := m.driver.Discover(ctx, id, previous)
		if err == nil && inst == nil {
			var created Instance
			created, err = m.driver.Start(ctx, id)
			if err == nil || (created.Owned && created.RunID != "") {
				inst = &created
			}
		}
		if inst != nil {
			err = errors.Join(err, m.record(id, *inst))
		}
		// Docker creates a stopped container first. Persist its identity before
		// activation so a failed start remains visible and can be recovered.
		if err == nil {
			if activator, ok := m.driver.(interface {
				Activate(context.Context, string, *Instance) error
			}); ok {
				err = activator.Activate(ctx, id, inst)
				err = errors.Join(err, m.record(id, *inst))
			}
		}
		if err == nil {
			m.view(id, "starting", "等待服务就绪")
			err = m.driver.Probe(ctx, id, *inst)
		}
		cancel()
		if err != nil {
			m.view(id, "failed", err.Error())
			m.finish(fmt.Errorf("%s启动未完成：%w", labels[id], err), "failed", "启动未完成；已运行的服务保留，可修复后重试或停止。")
			return
		}
		inst.Ready = true
		if err = m.record(id, *inst); err != nil {
			m.finish(err, "failed", "无法保存运行记录")
			return
		}
		detail := "本次启动"
		if !inst.Owned {
			detail = "复用已有实例，停止操作会保留它"
		}
		m.view(id, "ready", detail)
	}
	m.finish(nil, "ready", "后端已就绪，可以连接房间。")
}
func (m *Manager) retainedDependency(id string) bool {
	dependents := map[string][]string{"mysql": {"server", "consumer", "migrate"}, "redis": {"server"}, "kafka": {"server", "consumer", "kafka-init"}}
	for _, dep := range dependents[id] {
		if inst, ok := m.instance(dep); ok && !inst.Owned && m.driver.Alive(inst) {
			return true
		}
	}
	return false
}

// Keep the first stop boundary across retries and launcher recovery. Always
// save before issuing the consumer request, including after a failed save.
func (m *Manager) stopTargets(ctx context.Context) (map[int32]int64, error) {
	m.mu.Lock()
	captured := m.consumerTargets != nil
	m.mu.Unlock()
	if !captured {
		targets, err := m.driver.Targets(ctx)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.consumerTargets = make(map[int32]int64, len(targets))
		for p, n := range targets {
			m.consumerTargets[p] = n
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLocked(); err != nil {
		return nil, fmt.Errorf("保存消费者停止目标失败: %w", err)
	}
	targets := make(map[int32]int64, len(m.consumerTargets))
	for p, n := range m.consumerTargets {
		targets[p] = n
	}
	return targets, nil
}
func (m *Manager) stop() {
	var targets map[int32]int64
	retained := false
	for _, id := range []string{"server", "consumer", "migrate", "kafka-init", "kafka", "redis", "mysql"} {
		inst, ok := m.instance(id)
		if !ok {
			continue
		}
		if !inst.Owned {
			if m.driver.Alive(inst) {
				retained = true
				m.view(id, "ready", "原有实例，保留运行")
			}
			continue
		}
		if inst.Stopped {
			m.view(id, "stopped", "已确认停止")
			continue
		}
		// An owned business process must have an explicit successful stop result,
		// even when it has already exited; Driver.Stop verifies that result.
		if m.retainedDependency(id) {
			retained = true
			m.view(id, "ready", "保留：仍有原有业务服务依赖它")
			continue
		}
		if id != "server" && id != "consumer" && !isSetupJob(id) {
			if guard, ok := m.driver.(interface {
				BeforeStopDependency(context.Context, string) error
			}); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err := guard.BeforeStopDependency(ctx, id)
				cancel()
				if err != nil {
					retained = true
					m.view(id, "ready", "保留："+err.Error())
					continue
				}
			}
		}
		m.view(id, "stopping", "等待正常退出")
		ctx, cancel := context.WithTimeout(context.Background(), stopLimits[id])
		var err error
		if id == "consumer" {
			needTargets := m.driver.Alive(inst)
			if planner, ok := m.driver.(interface {
				RequiresTargets(context.Context, Instance) (bool, error)
			}); ok {
				needTargets, err = planner.RequiresTargets(ctx, inst)
			}
			if err == nil && needTargets {
				targets, err = m.stopTargets(ctx)
			}
		}
		if err == nil {
			err = m.driver.Stop(ctx, id, inst, targets)
		}
		cancel()
		if err != nil {
			m.view(id, "failed", err.Error())
			m.finish(fmt.Errorf("%s停止未完成：%w", labels[id], err), "stop_failed", "停止未完成，相关服务及依赖已保留；可重试停止。")
			return
		}
		inst.Ready = false
		inst.Stopped = true
		if err = m.record(id, inst); err != nil {
			m.finish(err, "stop_failed", "停止结果无法保存")
			return
		}
		m.view(id, "stopped", "已停止")
	}
	m.mu.Lock()
	m.stopping = false
	m.consumerTargets = nil
	m.mu.Unlock()
	message := "本次启动的服务已停止。"
	phase := "stopped"
	if retained {
		message = "本次可停止的服务已停止，已有实例或仍被依赖的服务保留运行。"
		phase = "partial"
	}
	m.finish(nil, phase, message)
}

// Refresh performs no startup, writes or migrations. It updates cached status
// for known instances only; HTTP GET never runs a probe itself.
func (m *Manager) Refresh(ctx context.Context) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return
	}
	generation := m.generation
	m.mu.Unlock()
	for _, id := range managedOrder {
		inst, ok := m.instance(id)
		if !ok {
			continue
		}
		m.mu.Lock()
		changed := m.busy || m.generation != generation
		m.mu.Unlock()
		if changed {
			return
		}
		if !m.driver.Alive(inst) {
			m.mu.Lock()
			if m.busy || m.generation != generation {
				m.mu.Unlock()
				return
			}
			v := m.views[id]
			if v.Phase != "stopped" {
				v.Phase = "unknown"
				v.Detail = "进程已退出或身份已改变，停止结果待确认"
				m.views[id] = v
				m.phase = "partial"
			}
			m.mu.Unlock()
			continue
		}
		if !inst.Ready {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := m.driver.Probe(probe, id, inst)
		cancel()
		m.mu.Lock()
		if m.busy || m.generation != generation {
			m.mu.Unlock()
			return
		}
		v := m.views[id]
		if err != nil {
			v.Phase = "failed"
			v.Detail = err.Error()
			m.phase = "partial"
		} else if v.Phase != "failed" || !m.stopping {
			v.Phase = "ready"
			v.Detail = "运行中"
		}
		m.views[id] = v
		m.mu.Unlock()
	}
}

func isSetupJob(id string) bool { return id == "migrate" || id == "kafka-init" }
