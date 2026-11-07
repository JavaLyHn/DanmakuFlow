package launcher

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

// DockerRuntime reuses the existing protocol probes and consumer drain boundary.
// Only the mechanism for creating and identifying a service is different.
type DockerRuntime struct {
	*Runtime
	run func(context.Context, ...string) ([]byte, error)
}
type dockerIntent struct{ Service, ConfigKey, RunID string }

func NewDockerRuntime(c Config, configErr error, stateDir string) *DockerRuntime {
	r := &DockerRuntime{Runtime: NewRuntime(c, configErr, stateDir)}
	r.run = r.command
	return r
}
func validHex(s string, n int) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == n }
func (r *DockerRuntime) Validate() error {
	if r.configErr != nil {
		return r.configErr
	}
	if r.config.Version != 1 || r.config.Backend != "docker" {
		return errors.New("Docker 本机配置无效，请运行 scripts/setup-docker.ps1")
	}
	if _, err := parseLocalDSN(r.dsn); err != nil {
		return err
	}
	d := r.config.Docker
	if d.Context != "desktop-linux" || d.Project != "danmakuflow-local" {
		return errors.New("仅支持本机 desktop-linux 上的 danmakuflow-local 项目")
	}
	for _, p := range []struct {
		path, label string
		dir         bool
	}{
		{r.config.RepoDir, "仓库目录", true}, {d.Executable, "Docker 程序", false}, {d.ComposeFile, "Compose 配置", false}, {d.EnvFile, "Docker 本机环境配置", false},
	} {
		if err := checkPath(p.path, p.label, p.dir); err != nil {
			return err
		}
	}
	return nil
}
func (r *DockerRuntime) Preflight(ctx context.Context, known map[string]Instance) error {
	b, err := r.run(ctx, "context", "inspect", r.config.Docker.Context, "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "npipe:////./pipe/") {
		return errors.New("Docker context 不是本机 Windows 管道，未启动服务")
	}
	b, err = r.run(ctx, "info", "--format", "{{.OSType}}")
	if err != nil {
		return errors.New("Docker Linux 引擎不可用，请先打开 Docker Desktop 并等待引擎就绪")
	}
	if strings.TrimSpace(string(b)) != "linux" {
		return errors.New("请将 Docker Desktop 切换为 Linux containers")
	}
	for _, id := range []string{"migrate", "kafka-init"} {
		c, err := r.find(ctx, id)
		if err != nil {
			return err
		}
		if c != nil && c.State.Status != "exited" {
			return errors.New("初始化任务尚未结束，请先停止本次服务")
		}
		if old, ok := known[id]; ok && old.Owned && !old.Stopped {
			return errors.New("初始化任务退出结果待确认，请先停止本次服务")
		}
	}
	for _, id := range serviceOrder {
		c, err := r.find(ctx, id)
		if err != nil {
			return err
		}
		if c != nil {
			inst := r.fromContainer(*c, false)
			if old, ok := known[id]; ok && !old.Stopped {
				inst = r.recoverReceipt(id, old, *c)
			}
			if err = r.verify(id, inst, *c); err != nil {
				return err
			}
			continue
		}
		if port, ok := ports[id]; ok {
			listeners, err := localproc.Listeners(port)
			if err != nil {
				return err
			}
			if len(listeners) > 0 {
				return fmt.Errorf("端口 %d 已被现有服务占用，未启动容器或停止占用者", port)
			}
		}
	}
	return nil
}
func (r *DockerRuntime) fromContainer(c dockerContainer, owned bool) Instance {
	runID := c.Config.Labels[dockerRunLabel]
	return Instance{Container: &ContainerIdentity{ID: c.ID, StartedAt: c.State.StartedAt}, Owned: owned, RunID: runID, RunDir: filepath.Join(r.stateDir, "runs", runID)}
}
func (r *DockerRuntime) Discover(ctx context.Context, id string, old *Instance) (*Instance, error) {
	c, err := r.find(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		if old != nil && old.Owned && !old.Stopped {
			return nil, errors.New("原容器已消失，退出结果未确认；保留现有依赖")
		}
		if id == "consumer" {
			if err = r.checkGroup(ctx, ""); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	inst := r.fromContainer(*c, false)
	if !validHex(inst.RunID, 16) {
		return nil, errors.New("已有容器不包含此启动器的运行标记，未认领或重建")
	}
	if old != nil && old.Container != nil && old.Container.ID == c.ID {
		inst = r.recoverReceipt(id, *old, *c)
	} else {
		var saved Instance
		if runcontrol.ReadJSON(filepath.Join(inst.RunDir, "container.json"), &saved) == nil && saved.Owned && r.validRun(saved) && r.verify(id, saved, *c) == nil {
			inst = saved
		} else if c.State.Status == "created" {
			// A compose create can finish after its CLI times out. A private
			// intent plus exact labels recovers only the never-started container.
			var intent dockerIntent
			if runcontrol.ReadJSON(filepath.Join(inst.RunDir, "intent.json"), &intent) == nil && intent == (dockerIntent{id, r.ConfigKey(), inst.RunID}) {
				inst.Owned = true
				if err = r.receipt(inst); err != nil {
					return nil, err
				}
			}
		}
	}
	if err = r.verify(id, inst, *c); err != nil {
		return nil, err
	}
	if c.State.Running {
		return &inst, nil
	}
	if c.State.Status == "created" && inst.Owned && !inst.Stopped {
		return &inst, nil
	}
	if inst.Owned && inst.Stopped && removableContainer(id, *c) {
		return nil, nil
	}
	return &inst, errors.New("已有停止容器的退出结果或所有权未确认；未自动重建，请先停止本次服务")
}
func (r *DockerRuntime) Start(ctx context.Context, id string) (Instance, error) {
	if _, ok := labels[id]; !ok {
		return Instance{}, errors.New("未知组件")
	}
	if c, err := r.find(ctx, id); err != nil {
		return Instance{}, err
	} else if c != nil {
		inst := r.fromContainer(*c, false)
		var saved Instance
		if runcontrol.ReadJSON(filepath.Join(inst.RunDir, "container.json"), &saved) != nil || !saved.Owned || !saved.Stopped || !r.validRun(saved) || r.verify(id, saved, *c) != nil || !removableContainer(id, *c) {
			return Instance{}, errors.New("已有容器未确认正常停止，未重建")
		}
		if _, err = r.run(ctx, "rm", c.ID); err != nil {
			return Instance{}, err
		}
	}
	runID, err := randomID()
	if err != nil {
		return Instance{}, err
	}
	inst := Instance{Owned: true, RunID: runID, RunDir: filepath.Join(r.stateDir, "runs", runID)}
	if err = localproc.SecureDir(inst.RunDir); err != nil {
		return Instance{}, err
	}
	if err = runcontrol.AtomicJSON(filepath.Join(inst.RunDir, "intent.json"), dockerIntent{id, r.ConfigKey(), runID}); err != nil {
		return Instance{}, err
	}
	service := map[string]any{"labels": map[string]string{dockerConfigLabel: r.ConfigKey(), dockerRunLabel: runID}}
	if id == "server" || id == "consumer" {
		service["environment"] = map[string]string{"DANMAKUFLOW_RUN_ID": runID, "DANMAKUFLOW_RUN_DIR": "/run/danmakuflow"}
		service["volumes"] = []any{map[string]any{"type": "bind", "source": inst.RunDir, "target": "/run/danmakuflow", "bind": map[string]bool{"create_host_path": false}}}
	}
	override := filepath.Join(inst.RunDir, "compose.json")
	if err = runcontrol.AtomicJSON(override, map[string]any{"services": map[string]any{id: service}}); err != nil {
		return Instance{}, err
	}
	_, createErr := r.run(ctx, r.composeArgs(override, "up", "--no-start", "--no-deps", "--no-build", "--no-recreate", "--pull", "never", id)...)
	// Inspect independently of a timed-out compose command, without issuing a
	// second create/start. Preserve the first error alongside ownership evidence.
	check, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, inspectErr := r.find(check, id)
	if inspectErr != nil || c == nil {
		return Instance{}, errors.Join(createErr, inspectErr, errors.New("未能确认容器创建结果；创建意图已保存，重试会先核对"))
	}
	inst.Container = &ContainerIdentity{ID: c.ID, StartedAt: c.State.StartedAt}
	if err = r.verify(id, inst, *c); err != nil {
		return Instance{}, err
	}
	if c.State.Status != "created" {
		return Instance{}, errors.New("新容器不是待启动状态，未认领正在运行的实例")
	}
	return inst, errors.Join(createErr, r.receipt(inst))
}
func (r *DockerRuntime) Activate(ctx context.Context, id string, inst *Instance) error {
	if !inst.Owned {
		return nil
	}
	if inst.Container == nil {
		return errors.New("缺少容器身份")
	}
	c, err := r.inspect(ctx, inst.Container.ID)
	if err != nil {
		return err
	}
	if err = r.verify(id, *inst, c); err != nil {
		return err
	}
	if c.State.Running {
		return nil
	}
	if c.State.Status != "created" {
		return errors.New("容器不是待启动状态，未自动重新运行")
	}
	_, startErr := r.run(ctx, "start", c.ID)
	check, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err = r.inspect(check, c.ID)
	if err != nil {
		return errors.Join(startErr, err)
	}
	inst.Container = &ContainerIdentity{ID: c.ID, StartedAt: c.State.StartedAt}
	if err = r.verify(id, *inst, c); err != nil {
		return errors.Join(startErr, err)
	}
	return errors.Join(startErr, r.receipt(*inst))
}
func (r *DockerRuntime) Alive(inst Instance) bool {
	if inst.Container == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := r.inspect(ctx, inst.Container.ID)
	if err != nil {
		return false
	}
	id := c.Config.Labels["com.docker.compose.service"]
	return r.verify(id, inst, c) == nil && c.State.Running
}
func (r *DockerRuntime) Probe(ctx context.Context, id string, inst Instance) error {
	if isSetupJob(id) {
		return r.waitSetupJob(ctx, id, inst)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if inst.Container == nil {
			return errors.New("缺少容器身份")
		}
		c, err := r.inspect(ctx, inst.Container.ID)
		if err != nil {
			return err
		}
		if err = r.verify(id, inst, c); err != nil {
			return err
		}
		if !c.State.Running {
			return fmt.Errorf("容器已退出（退出码 %d），请查看日志", c.State.ExitCode)
		}
		if err = r.dockerProbeOnce(ctx, id, inst); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return r.safe(fmt.Errorf("%v：%w", err, ctx.Err()))
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (r *DockerRuntime) dockerProbeOnce(ctx context.Context, id string, inst Instance) error {
	if id == "server" || id == "consumer" {
		s, err := runcontrol.ReadStatus(inst.RunDir, inst.RunID)
		if err != nil {
			return err
		}
		if s.PID != 1 || s.Component != id {
			return errors.New("容器业务进程标记不符")
		}
		if s.Phase != "ready" {
			return fmt.Errorf("等待容器业务就绪（%s）：%s", s.Phase, s.Error)
		}
		if id == "consumer" {
			return r.checkGroup(ctx, inst.RunID, true)
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/", nil)
		client := http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("容器页面返回 HTTP %d", response.StatusCode)
		}
		return nil
	}
	return r.probeProtocol(ctx, id, inst)
}
