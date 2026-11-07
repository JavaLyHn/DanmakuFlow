package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const dockerConfigLabel = "io.danmakuflow.config"
const dockerRunLabel = "io.danmakuflow.run"

type dockerContainer struct {
	ID     string `json:"Id"`
	Config struct{ Labels map[string]string }
	State  struct {
		Status                         string
		Running, Restarting, OOMKilled bool
		ExitCode                       int
		StartedAt, FinishedAt          string
	}
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 1024*1024 - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func (r *DockerRuntime) command(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.config.Docker.Executable, append([]string{"--context", r.config.Docker.Context}, args...)...)
	cmd.Dir = r.config.RepoDir
	for _, v := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(v, "=", 2)[0])
		// Host DSN must not override the container DSN from --env-file.
		if strings.HasPrefix(key, "DANMAKUFLOW_") || key == "MYSQL_ROOT_PASSWORD" || strings.HasPrefix(key, "COMPOSE_") {
			continue
		}
		cmd.Env = append(cmd.Env, v)
	}
	cmd.Env = append(cmd.Env, "COMPOSE_PARALLEL_LIMIT=1")
	hideDockerCommand(cmd)
	var stdout, stderr cappedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), r.safe(fmt.Errorf("Docker 操作失败：%w；%s", err, strings.TrimSpace(stderr.String())))
	}
	if len(args) > 0 && args[0] == "logs" {
		return append(stdout.Bytes(), stderr.Bytes()...), nil
	}
	return stdout.Bytes(), nil
}
func (r *DockerRuntime) composeArgs(override string, args ...string) []string {
	d := r.config.Docker
	out := []string{"compose", "--project-name", d.Project, "--project-directory", r.config.RepoDir, "--env-file", d.EnvFile, "--file", d.ComposeFile}
	if override != "" {
		out = append(out, "--file", override)
	}
	out = append(out, "--parallel", "1")
	return append(out, args...)
}
func (r *DockerRuntime) inspect(ctx context.Context, cid string) (dockerContainer, error) {
	var zero dockerContainer
	if !validHex(cid, 32) {
		return zero, errors.New("容器 ID 无效")
	}
	b, err := r.run(ctx, "inspect", "--type", "container", cid)
	if err != nil {
		return zero, err
	}
	var items []dockerContainer
	if json.Unmarshal(b, &items) != nil || len(items) != 1 || items[0].ID != cid {
		return zero, errors.New("无法确认 Docker 容器身份")
	}
	return items[0], nil
}
func (r *DockerRuntime) find(ctx context.Context, id string) (*dockerContainer, error) {
	if _, ok := labels[id]; !ok {
		return nil, errors.New("未知组件")
	}
	b, err := r.run(ctx, "ps", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+r.config.Docker.Project, "--filter", "label=com.docker.compose.service="+id, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) != 1 {
		return nil, errors.New("发现多个同名服务容器，未认领或停止它们")
	}
	c, err := r.inspect(ctx, ids[0])
	return &c, err
}
func (r *DockerRuntime) verify(id string, inst Instance, c dockerContainer) error {
	if inst.Container == nil || c.ID != inst.Container.ID || c.State.StartedAt != inst.Container.StartedAt || c.State.Restarting {
		if inst.Container != nil && c.ID == inst.Container.ID && (inst.Container.StartedAt == "" || inst.Container.StartedAt == "0001-01-01T00:00:00Z") {
			return errors.New("上次容器启动时间未能保存，需要人工核对 Docker 运行记录；未自动认领当前实例")
		}
		return errors.New("容器已被替换或重新启动，未操作新的实例")
	}
	l := c.Config.Labels
	if l["com.docker.compose.project"] != r.config.Docker.Project || l["com.docker.compose.service"] != id || l[dockerConfigLabel] != r.ConfigKey() || l[dockerRunLabel] != inst.RunID {
		return errors.New("容器归属与本机记录不符，未操作该容器")
	}
	return nil
}

// A receipt can be newer than Manager state after activation or shutdown. It
// is usable only when every identity field still matches the actual container.
func (r *DockerRuntime) recoverReceipt(id string, inst Instance, c dockerContainer) Instance {
	var saved Instance
	if runcontrol.ReadJSON(filepath.Join(inst.RunDir, "container.json"), &saved) == nil && saved.Owned && r.validRun(saved) && r.verify(id, saved, c) == nil {
		if inst.Ready && !saved.Stopped {
			saved.Ready = true
		}
		return saved
	}
	return inst
}
func (r *DockerRuntime) receipt(inst Instance) error {
	return runcontrol.AtomicJSON(filepath.Join(inst.RunDir, "container.json"), inst)
}
