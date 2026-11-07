package launcher

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Callers must first verify the receipt and its Stopped result. This predicate
// permits replacing a settled failed startup; it does not establish ownership.
func removableContainer(_ string, c dockerContainer) bool {
	return !c.State.Running && !c.State.Restarting && (c.State.Status == "created" || c.State.Status == "exited")
}

func (r *DockerRuntime) waitSetupJob(ctx context.Context, id string, inst Instance) error {
	return r.waitSetupExit(ctx, id, inst, true)
}

func (r *DockerRuntime) waitSetupExit(ctx context.Context, id string, inst Instance, successRequired bool) error {
	if !isSetupJob(id) || inst.Container == nil {
		return errors.New("初始化任务或容器身份无效")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := r.inspect(ctx, inst.Container.ID)
		if err != nil {
			return err
		}
		if err = r.verify(id, inst, c); err != nil {
			return err
		}
		if !c.State.Running {
			switch c.State.Status {
			case "created":
				if successRequired {
					return errors.New("初始化任务尚未执行，不能认定成功")
				}
				return nil
			case "exited":
				if successRequired && (c.State.ExitCode != 0 || c.State.OOMKilled) {
					return fmt.Errorf("初始化任务失败（退出码 %d，OOM=%t），请查看该任务日志", c.State.ExitCode, c.State.OOMKilled)
				}
				return nil
			default:
				return fmt.Errorf("初始化任务退出状态无法确认：%s", c.State.Status)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Only the explicit initialization path bypasses schema/topic readiness. The
// embedded runtime keeps normal startup, identity checks and shutdown behavior.
type dockerSetupDriver struct{ *DockerRuntime }

func (r *dockerSetupDriver) Probe(ctx context.Context, id string, inst Instance) error {
	if isSetupJob(id) {
		return r.waitSetupJob(ctx, id, inst)
	}
	if id != "mysql" && id != "redis" && id != "kafka" {
		return errors.New("初始化不启动业务服务")
	}
	if inst.Container == nil {
		return errors.New("缺少容器身份")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := r.inspect(ctx, inst.Container.ID)
		if err != nil {
			return err
		}
		if err = r.verify(id, inst, c); err != nil {
			return err
		}
		if !c.State.Running {
			return fmt.Errorf("初始化所需容器已退出（退出码 %d）", c.State.ExitCode)
		}
		if err = r.infrastructureReady(ctx, id); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return r.safe(fmt.Errorf("等待基础服务就绪：%v：%w", err, ctx.Err()))
		case <-time.After(250 * time.Millisecond):
		}
	}
}
