package launcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

func cleanContainerExit(c dockerContainer) bool {
	return !c.State.Running && !c.State.Restarting && !c.State.OOMKilled && (c.State.Status == "created" || (c.State.Status == "exited" && c.State.ExitCode == 0))
}
func normalContainerExit(id string, inst Instance, c dockerContainer) bool {
	return cleanContainerExit(c) || (id == "kafka" && inst.Container != nil && inst.Container.TermRequested && !c.State.Running && !c.State.Restarting && !c.State.OOMKilled && c.State.Status == "exited" && c.State.ExitCode == 143)
}
func (r *DockerRuntime) stopped(inst Instance) error {
	inst.Stopped = true
	inst.Ready = false
	return r.receipt(inst)
}
func (r *DockerRuntime) Stop(ctx context.Context, id string, inst Instance, targets map[int32]int64) error {
	if !inst.Owned {
		return errors.New("不允许停止原有容器")
	}
	if !r.validRun(inst) || inst.Container == nil {
		return errors.New("容器运行记录无效，未操作容器")
	}
	c, err := r.inspect(ctx, inst.Container.ID)
	if err != nil {
		return err
	}
	if err = r.verify(id, inst, c); err != nil {
		inst = r.recoverReceipt(id, inst, c)
		if err = r.verify(id, inst, c); err != nil {
			return err
		}
	}
	inst = r.recoverReceipt(id, inst, c)
	if inst.Stopped {
		if !removableContainer(id, c) {
			return errors.New("已停容器状态变化，未操作")
		}
		return nil
	}
	if c.State.Status == "created" && !c.State.Running {
		return r.stopped(inst)
	}
	if isSetupJob(id) {
		if c.State.Running {
			if _, err = r.run(ctx, "kill", "--signal=SIGTERM", inst.Container.ID); err != nil {
				return err
			}
		}
		if err = r.waitSetupExit(ctx, id, inst, false); err != nil {
			return err
		}
		return r.stopped(inst)
	}
	if id == "server" || id == "consumer" {
		if err = r.stopBusinessContainer(ctx, id, inst, c, targets); err != nil {
			return err
		}
		return r.stopped(inst)
	}
	if !c.State.Running {
		if !inst.Ready && c.State.Status == "exited" {
			return r.stopped(inst)
		}
		if !normalContainerExit(id, inst, c) {
			return fmt.Errorf("容器异常退出（退出码 %d），依赖保留", c.State.ExitCode)
		}
		return r.stopped(inst)
	}
	if !inst.Ready {
		if err = r.infrastructureReady(ctx, id); err != nil {
			return fmt.Errorf("容器初始化尚未完成，保留运行：%w", err)
		}
	}
	// Unlike docker stop's timeout fallback, this sends ONLY SIGTERM. A failed
	// graceful shutdown leaves the container and its dependencies running.
	identity := *inst.Container
	identity.TermRequested = true
	inst.Container = &identity
	if err = r.receipt(inst); err != nil {
		return err
	}
	if _, err = r.run(ctx, "kill", "--signal=SIGTERM", inst.Container.ID); err != nil {
		return err
	}
	if err = r.waitContainerExit(ctx, id, inst); err != nil {
		return err
	}
	return r.stopped(inst)
}
func (r *DockerRuntime) infrastructureReady(ctx context.Context, id string) error {
	switch id {
	case "mysql":
		return mysqlGreeting(ctx, "127.0.0.1:3306")
	case "redis":
		c, err := redisConnection(ctx)
		if err != nil {
			return err
		}
		return c.Close()
	case "kafka":
		c, err := kafkaClient(ctx)
		if err != nil {
			return err
		}
		return c.Close()
	}
	return errors.New("未知容器组件")
}
func (r *DockerRuntime) waitContainerExit(ctx context.Context, id string, inst Instance) error {
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
			if !normalContainerExit(id, inst, c) {
				return fmt.Errorf("容器未正常退出（退出码 %d），依赖保留", c.State.ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}
func (r *DockerRuntime) stopBusinessContainer(ctx context.Context, id string, inst Instance, c dockerContainer, targets map[int32]int64) error {
	s, readErr := runcontrol.ReadStatus(inst.RunDir, inst.RunID)
	matching := readErr == nil && s.PID == 1 && s.Component == id
	if matching && s.Phase == "stopped" {
		if id == "consumer" && s.EverReady {
			if err := r.checkCommittedTargets(ctx, targets); err != nil {
				return err
			}
		}
		return r.waitContainerExit(ctx, id, inst)
	}
	if !c.State.Running {
		if !inst.Ready && matching && !s.EverReady && c.State.Status == "exited" {
			return nil
		}
		return errors.New("业务容器退出但未确认消息排空，依赖保留")
	}
	if !matching {
		return errors.New("业务容器运行标记未就绪，未发送停止请求")
	}
	if id == "consumer" {
		if targets == nil {
			return errors.New("消费者停止目标尚未保存，依赖保留；请重试停止")
		}
		if err := r.checkGroup(ctx, inst.RunID); err != nil {
			return err
		}
	}
	requestID, err := randomID()
	if err != nil {
		return err
	}
	limit := stopLimits[id]
	if deadline, ok := ctx.Deadline(); ok {
		limit = time.Until(deadline) - 100*time.Millisecond
	}
	if limit <= 0 {
		return context.DeadlineExceeded
	}
	if err = runcontrol.WriteRequest(inst.RunDir, inst.RunID, runcontrol.Request{ID: requestID, Action: "stop", Targets: targets, TimeoutMillis: limit.Milliseconds()}); err != nil {
		return err
	}
	for {
		c, err = r.inspect(ctx, inst.Container.ID)
		if err != nil {
			return err
		}
		if err = r.verify(id, inst, c); err != nil {
			return err
		}
		s, err = runcontrol.ReadStatus(inst.RunDir, inst.RunID)
		if err == nil && s.PID == 1 && s.Component == id && s.RequestID == requestID {
			if s.Phase == "stop_failed" {
				return r.safe(errors.New(s.Error))
			}
			if s.Phase == "stopped" {
				if id == "consumer" {
					if err = r.checkCommittedTargets(ctx, targets); err != nil {
						return err
					}
				}
				return r.waitContainerExit(ctx, id, inst)
			}
		}
		if !c.State.Running {
			return errors.New("业务容器退出但未确认本次停止结果，依赖保留")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// An unavailable Docker inspection is an error, never evidence that the
// consumer no longer needs a durable drain boundary.
func (r *DockerRuntime) RequiresTargets(ctx context.Context, inst Instance) (bool, error) {
	if inst.Container == nil {
		return false, errors.New("缺少消费者容器身份")
	}
	c, err := r.inspect(ctx, inst.Container.ID)
	if err != nil {
		return false, err
	}
	inst = r.recoverReceipt("consumer", inst, c)
	if err = r.verify("consumer", inst, c); err != nil {
		return false, err
	}
	if c.State.Running || inst.Ready {
		return true, nil
	}
	if c.State.Status == "created" {
		return false, nil
	}
	s, err := runcontrol.ReadStatus(inst.RunDir, inst.RunID)
	if err != nil || s.Component != "consumer" || s.PID != 1 {
		return false, errors.New("无法核对消费者是否曾经就绪")
	}
	return s.EverReady, nil
}
func (r *DockerRuntime) checkCommittedTargets(ctx context.Context, targets map[int32]int64) error {
	if targets == nil {
		return errors.New("消费者停止目标尚未保存，依赖保留；请重试停止")
	}
	c, err := kafkaClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	b, err := c.Coordinator(group)
	if err != nil {
		return err
	}
	req := &sarama.OffsetFetchRequest{Version: 1, ConsumerGroup: group}
	for p := range targets {
		req.AddPartition(topic, p)
	}
	resp, err := b.FetchOffset(req)
	if err != nil {
		return err
	}
	for p, target := range targets {
		block := resp.GetBlock(topic, p)
		if block == nil || block.Err != sarama.ErrNoError || (block.Offset < target && !(target == 0 && block.Offset == -1)) {
			return errors.New("消费者退出但未提交全部停止目标，保留依赖")
		}
	}
	return nil
}
func (r *DockerRuntime) BeforeStopDependency(ctx context.Context, id string) error {
	for _, business := range []string{"server", "consumer", "migrate", "kafka-init"} {
		if id == "redis" && business == "consumer" {
			continue
		}
		c, err := r.find(ctx, business)
		if err != nil {
			return err
		}
		if c != nil && (c.State.Running || c.State.Restarting || (isSetupJob(business) && c.State.Status != "exited")) {
			return fmt.Errorf("仍有 %s 容器运行，保留依赖", labels[business])
		}
	}
	listeners, err := localproc.Listeners(8080)
	if err != nil {
		return err
	}
	if len(listeners) > 0 {
		return errors.New("仍有弹幕服务监听 8080，保留依赖")
	}
	if id != "redis" {
		c, err := r.find(ctx, "kafka")
		if err != nil {
			return err
		}
		if c != nil && c.State.Running {
			if err = r.checkGroup(ctx, ""); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *DockerRuntime) LogTail(id string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := r.find(ctx, id)
	if err != nil {
		return "", err
	}
	if c == nil {
		return "尚无容器日志", nil
	}
	inst := r.fromContainer(*c, false)
	if err = r.verify(id, inst, *c); err != nil {
		return "", err
	}
	b, err := r.run(ctx, "logs", "--tail", "80", c.ID)
	if err != nil {
		return "", err
	}
	if len(b) > 8192 {
		b = b[len(b)-8192:]
	}
	return r.redact(string(b)), nil
}
func (r *DockerRuntime) TrimLogs() {} // Docker's bounded log driver owns rotation.
