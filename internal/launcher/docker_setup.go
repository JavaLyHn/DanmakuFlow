package launcher

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var dockerSetupOrder = []string{"mysql", "redis", "kafka", "migrate", "kafka-init"}

// InitializeDocker is called only by the explicit CLI flag, while the launcher
// owns port 4174. It uses the same instances file and receipts as the local page.
func (r *DockerRuntime) InitializeDocker(ctx context.Context, stateFile string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	m, err := NewManager(&dockerSetupDriver{DockerRuntime: r}, stateFile, r.ConfigKey())
	if err != nil {
		return err
	}
	// Recovery writes local ownership evidence only. No container is created,
	// activated or stopped until every existing service has been inspected.
	recoverCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = r.recoverSetupInstances(recoverCtx, m)
	cancel()
	if err != nil {
		return err
	}
	if m.stopping {
		return errors.New("上次停止尚未完成，请先通过本地页面停止本次服务")
	}
	for _, inst := range m.instances {
		if inst.Owned && !inst.Stopped {
			return errors.New("存在未确认停止的本次服务，请先通过本地页面停止后再初始化")
		}
	}
	return runDockerInitialization(ctx, m)
}

func (r *DockerRuntime) recoverSetupInstances(ctx context.Context, m *Manager) error {
	var problems []error
	for _, id := range managedOrder {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(problems, err)...)
		}
		c, err := r.find(ctx, id)
		if err != nil {
			problems = append(problems, fmt.Errorf("检查%s：%w", labels[id], err))
			continue
		}
		if c == nil {
			// In particular, do not discover a missing consumer: that ordinary
			// startup path contacts a Kafka coordinator which may not exist yet.
			continue
		}
		old, ok := m.instance(id)
		var previous *Instance
		if ok {
			previous = &old
		}
		inst, discoverErr := r.Discover(ctx, id, previous)
		if inst != nil {
			if err = m.record(id, *inst); err != nil {
				return errors.Join(append(problems, err)...)
			}
			if inst.Owned && !inst.Stopped {
				problems = append(problems, fmt.Errorf("%s存在恢复出的未停止实例，请先在本地页面停止本次服务", labels[id]))
			}
		}
		if discoverErr != nil {
			problems = append(problems, fmt.Errorf("恢复%s：%w", labels[id], discoverErr))
		}
		if (id == "server" || id == "consumer" || isSetupJob(id)) && (c.State.Running || c.State.Restarting || c.State.Status == "created") {
			problems = append(problems, fmt.Errorf("%s已有活动实例，未执行初始化或停止该实例", labels[id]))
		}
	}
	return errors.Join(problems...)
}

func runDockerInitialization(ctx context.Context, m *Manager) error {
	if err := m.driver.Validate(); err != nil {
		return err
	}
	if err := m.begin("initialize"); err != nil {
		return err
	}
	limits := make(map[string]time.Duration, len(dockerSetupOrder))
	for _, id := range dockerSetupOrder {
		limits[id] = 300 * time.Second
	}
	m.startServices(ctx, dockerSetupOrder, limits)
	s := m.Snapshot()
	if s.Error != "" {
		return errors.New(s.Error)
	}
	if s.Phase != "ready" {
		return fmt.Errorf("初始化未完成（状态 %s）；请在本地页面检查并停止残留服务", s.Phase)
	}
	// All previous owned instances were refused before startup. This normal
	// stop therefore affects only this initialization's owned containers.
	if err := m.begin("stop"); err != nil {
		return err
	}
	m.stop()
	s = m.Snapshot()
	if s.Error != "" {
		return errors.New(s.Error)
	}
	for _, id := range dockerSetupOrder {
		if inst, ok := m.instance(id); ok && inst.Owned && !inst.Stopped {
			err := errors.New("初始化任务已完成，但本次基础服务仍被依赖；请在本地页面完成停止")
			m.mu.Lock()
			m.stopping = true
			m.mu.Unlock()
			m.finish(err, "stop_failed", err.Error())
			return err
		}
	}
	if s.Phase != "stopped" && s.Phase != "partial" {
		return fmt.Errorf("初始化后的停止结果未确认（状态 %s）", s.Phase)
	}
	return nil
}
