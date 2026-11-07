package launcher

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/infra"
	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

const topic = "danmaku_save_topic"
const group = "danmaku_group_v1"

var ports = map[string]int{"mysql": 3306, "redis": 6379, "kafka": 9092, "server": 8080}

type Runtime struct {
	config        Config
	configErr     error
	dsn, stateDir string
}

func NewRuntime(c Config, configErr error, stateDir string) *Runtime {
	return &Runtime{config: c, configErr: configErr, dsn: os.Getenv("DANMAKUFLOW_MYSQL_DSN"), stateDir: stateDir}
}
func (r *Runtime) ConfigKey() string { db, _ := parseLocalDSN(r.dsn); return configKey(r.config, db) }
func (r *Runtime) Validate() error {
	if r.configErr != nil {
		return r.configErr
	}
	if r.config.Version != 1 {
		return errors.New("本机配置版本无效，请重新运行 setup-local.ps1")
	}
	if _, err := parseLocalDSN(r.dsn); err != nil {
		return err
	}
	for _, item := range []struct {
		path, label string
		dir         bool
	}{
		{r.config.RepoDir, "仓库目录", true}, {r.config.BinDir, "编译目录", true}, {filepath.Join(r.config.BinDir, "server.exe"), "server.exe", false}, {filepath.Join(r.config.BinDir, "consumer.exe"), "consumer.exe", false},
		{r.config.MySQL.Executable, "MySQL 程序", false}, {r.config.MySQL.ConfigFile, "MySQL 配置", false}, {r.config.Redis.Executable, "Redis 程序", false}, {r.config.Redis.ConfigFile, "Redis 配置", false}, {r.config.Redis.WorkingDir, "Redis 工作目录", true},
		{r.config.Kafka.JavaExecutable, "Java 程序", false}, {r.config.Kafka.Home, "Kafka 目录", true}, {r.config.Kafka.ConfigFile, "Kafka 配置", false},
	} {
		if err := checkPath(item.path, item.label, item.dir); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runtime) redact(s string) string {
	if r.dsn != "" {
		s = strings.ReplaceAll(s, r.dsn, "[数据库连接已隐藏]")
	}
	if c, err := parseLocalDSN(r.dsn); err == nil && c.Passwd != "" {
		s = strings.ReplaceAll(s, c.Passwd, "[隐藏]")
	}
	return s
}
func (r *Runtime) safe(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(r.redact(err.Error()))
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (r *Runtime) Alive(inst Instance) bool { return localproc.Matches(inst.Identity) }
func (r *Runtime) expectedExe(id string) string {
	switch id {
	case "mysql":
		return r.config.MySQL.Executable
	case "redis":
		return r.config.Redis.Executable
	case "kafka":
		return r.config.Kafka.JavaExecutable
	default:
		return filepath.Join(r.config.BinDir, id+".exe")
	}
}
func (r *Runtime) configMatches(id string, identity localproc.Identity) bool {
	if !samePath(identity.Executable, r.expectedExe(id)) || !localproc.Matches(identity) {
		return false
	}
	args, err := localproc.ProcessArgs(identity.PID)
	if err != nil || !localproc.Matches(identity) {
		return false
	}
	switch id {
	case "mysql":
		for _, arg := range args {
			if strings.HasPrefix(strings.ToLower(arg), "--defaults-file=") && samePath(arg[len("--defaults-file="):], r.config.MySQL.ConfigFile) {
				return true
			}
		}
	case "redis":
		for _, arg := range args {
			if samePath(arg, r.config.Redis.ConfigFile) {
				return true
			}
		}
	case "kafka":
		for i, arg := range args {
			if arg == "kafka.Kafka" && i+1 < len(args) && samePath(args[i+1], r.config.Kafka.ConfigFile) {
				return true
			}
		}
	}
	return false
}
func (r *Runtime) Discover(ctx context.Context, id string, old *Instance) (*Instance, error) {
	if old != nil && r.Alive(*old) {
		if !samePath(old.Identity.Executable, r.expectedExe(id)) {
			return nil, errors.New("运行程序与当前配置不符")
		}
		if err := localproc.Track(old.Identity); err != nil {
			return nil, err
		}
		return old, nil
	}
	if old != nil && old.Owned && !old.Stopped && (id == "server" || id == "consumer") {
		state, err := runcontrol.ReadStatus(old.RunDir, old.RunID)
		if !settledBusinessExit(id, *old, state, err) {
			return nil, errors.New("上次业务进程异常退出或结果未确认，请检查本机日志和进程记录")
		}
	}
	if id == "consumer" {
		if err := r.checkGroup(ctx, ""); err != nil {
			return nil, err
		}
		return nil, nil
	}
	ids, err := localproc.Listeners(ports[id])
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) != 1 || id == "server" || !r.configMatches(id, ids[0]) {
		return nil, fmt.Errorf("端口 %d 已被其他或无法确认的实例占用；未启动或停止任何占用者", ports[id])
	}
	return &Instance{Identity: ids[0], Owned: false}, nil
}
func (r *Runtime) Start(ctx context.Context, id string) (Instance, error) {
	if err := ctx.Err(); err != nil {
		return Instance{}, err
	}
	runID, err := randomID()
	if err != nil {
		return Instance{}, err
	}
	dir := filepath.Join(r.stateDir, "runs", runID)
	if err = localproc.SecureDir(dir); err != nil {
		return Instance{}, err
	}
	logDir := filepath.Join(r.stateDir, "logs")
	if err = localproc.SecureDir(logDir); err != nil {
		return Instance{}, err
	}
	out, err := os.OpenFile(filepath.Join(logDir, id+".stdout.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return Instance{}, err
	}
	defer out.Close()
	stderr, err := os.OpenFile(filepath.Join(logDir, id+".stderr.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return Instance{}, err
	}
	defer stderr.Close()
	env := make([]string, 0, len(os.Environ())+4)
	for _, v := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(v, "=", 2)[0])
		if key != "DANMAKUFLOW_MYSQL_DSN" && key != "DANMAKUFLOW_RUN_DIR" && key != "DANMAKUFLOW_RUN_ID" && key != "GOMAXPROCS" {
			env = append(env, v)
		}
	}
	env = append(env, "GOMAXPROCS=2")
	o := localproc.StartOptions{Executable: r.expectedExe(id), Dir: r.config.RepoDir, Env: env, Stdout: out, Stderr: stderr}
	switch id {
	case "mysql":
		o.Console = true
		// The Windows monitor otherwise forks a different listening PID and
		// shares its console; the launcher already owns this process lifecycle.
		o.Args = []string{"--defaults-file=" + r.config.MySQL.ConfigFile, "--console", "--no-monitor"}
	case "redis":
		// MSYS Redis treats a drive-letter positional config as a relative
		// POSIX path. Include loads the absolute Windows path without that rewrite.
		o.Args = []string{"--include", filepath.ToSlash(r.config.Redis.ConfigFile)}
		o.Dir = r.config.Redis.WorkingDir
	case "kafka":
		o.Console = true
		o.Dir = r.config.Kafka.Home
		o.Args = []string{"-Xms64m", "-Xmx192m", "-XX:+UseG1GC", "-XX:MaxDirectMemorySize=64m", "-XX:ActiveProcessorCount=2", "-Djava.awt.headless=true", "-Dkafka.logs.dir=" + logDir, "-Dlog4j2.configurationFile=" + filepath.Join(r.config.Kafka.Home, "config", "log4j2.yaml"), "-cp", filepath.Join(r.config.Kafka.Home, "libs", "*"), "kafka.Kafka", r.config.Kafka.ConfigFile}
	case "consumer", "server":
		o.Env = append(o.Env, "DANMAKUFLOW_MYSQL_DSN="+r.dsn, "DANMAKUFLOW_RUN_DIR="+dir, "DANMAKUFLOW_RUN_ID="+runID)
		if id == "server" {
			o.Args = []string{"-port", "8080", "-web-dir", filepath.Join(r.config.RepoDir, "web", "dist")}
		}
	default:
		return Instance{}, errors.New("不支持的组件")
	}
	identity, err := localproc.Start(o)
	if err != nil {
		return Instance{}, r.safe(fmt.Errorf("程序未能启动（请检查路径或 Windows 应用控制提示）：%w", err))
	}
	return Instance{Identity: identity, Owned: true, RunID: runID, RunDir: dir}, nil
}
func kafkaClient(ctx context.Context) (sarama.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_1_0_0
	cfg.ClientID = "danmakuflow-launcher"
	cfg.Metadata.AllowAutoTopicCreation = false
	cfg.Metadata.Retry.Max = 0
	cfg.Metadata.RefreshFrequency = 0
	cfg.Net.DialTimeout = time.Second
	cfg.Net.ReadTimeout = time.Second
	cfg.Net.WriteTimeout = time.Second
	return sarama.NewClient([]string{"127.0.0.1:9092"}, cfg)
}
func (r *Runtime) checkGroup(ctx context.Context, runID string, requireMember ...bool) error {
	return retryCoordinatorCheck(ctx, 250*time.Millisecond, func() error {
		return r.checkGroupOnce(ctx, runID, requireMember...)
	})
}
func retryCoordinatorCheck(ctx context.Context, interval time.Duration, check func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := check()
		if err == nil {
			return ctx.Err()
		}
		// A newly created offsets topic can still be loading after the broker
		// accepts metadata requests. Never retry ownership or authorization errors.
		if !errors.Is(err, sarama.ErrConsumerCoordinatorNotAvailable) &&
			!errors.Is(err, sarama.ErrNotCoordinatorForConsumer) &&
			!errors.Is(err, sarama.ErrOffsetsLoadInProgress) {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("等待 Kafka 消费组协调器就绪（%v）：%w", err, ctx.Err())
		case <-timer.C:
		}
	}
}
func (r *Runtime) checkGroupOnce(ctx context.Context, runID string, requireMember ...bool) error {
	c, err := kafkaClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	broker, err := c.Coordinator(group)
	if err != nil {
		return err
	}
	response, err := broker.DescribeGroups(&sarama.DescribeGroupsRequest{Version: 1, Groups: []string{group}})
	if err != nil {
		return err
	}
	if len(response.Groups) != 1 {
		return errors.New("无法确认 Kafka 消费组状态")
	}
	return validateGroup(response.Groups[0], runID, len(requireMember) > 0 && requireMember[0])
}
func validateGroup(g *sarama.GroupDescription, runID string, requireMember bool) error {
	if g.Err == sarama.ErrGroupIDNotFound && !requireMember {
		return nil
	}
	if g.Err != sarama.ErrNoError {
		return g.Err
	}
	if requireMember && len(g.Members) == 0 {
		return errors.New("消费者尚未加入消费组")
	}
	for _, member := range g.Members {
		if runID == "" || member.ClientId != "danmakuflow-"+runID {
			return errors.New("消费组内已有无法归属的消费者，已阻止重复启动")
		}
	}
	return nil
}
func (r *Runtime) Probe(ctx context.Context, id string, inst Instance) error {
	var last error
	for {
		if !r.Alive(inst) {
			return errors.New("进程已退出或身份已改变，请查看该组件日志")
		}
		last = r.probeOnce(ctx, id, inst)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return r.safe(fmt.Errorf("%v：%w", last, ctx.Err()))
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func listenerMatches(id string, inst Instance) error {
	items, err := localproc.Listeners(ports[id])
	if err != nil {
		return err
	}
	if len(items) != 1 || items[0].PID != inst.Identity.PID || items[0].Created != inst.Identity.Created {
		return errors.New("服务尚未监听，或监听进程与记录不一致")
	}
	return nil
}
func (r *Runtime) probeOnce(ctx context.Context, id string, inst Instance) error {
	if id != "consumer" {
		if err := listenerMatches(id, inst); err != nil {
			return err
		}
	}
	switch id {
	case "mysql":
		db, err := infra.OpenDB()
		if err != nil {
			return err
		}
		pool, err := db.DB()
		if err != nil {
			return err
		}
		pool.SetMaxOpenConns(1)
		defer pool.Close()
		return infra.CheckSchema(db.WithContext(ctx))
	case "redis":
		conn, err := redisConnection(ctx)
		if err != nil {
			return err
		}
		return conn.Close()
	case "kafka":
		c, err := kafkaClient(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		ps, err := c.Partitions(topic)
		if err != nil {
			return fmt.Errorf("检查 topic %s（启动器不会自动创建）：%w", topic, err)
		}
		if len(ps) == 0 {
			return errors.New("Kafka topic 没有分区")
		}
		for _, p := range ps {
			if _, err = c.Leader(topic, p); err != nil {
				return err
			}
		}
		return nil
	case "server", "consumer":
		s, err := runcontrol.ReadStatus(inst.RunDir, inst.RunID)
		if err != nil {
			return err
		}
		if s.PID != inst.Identity.PID || s.Component != id {
			return errors.New("就绪标记与进程不符")
		}
		if s.Phase != "ready" {
			if s.Error != "" {
				return errors.New(s.Error)
			}
			return fmt.Errorf("等待进程就绪（%s）", s.Phase)
		}
		if id == "consumer" {
			return r.checkGroup(ctx, inst.RunID, true)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/", nil)
		client := http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("页面返回 HTTP %d，请先构建 web/dist", resp.StatusCode)
		}
		return nil
	}
	return errors.New("未知组件")
}
func redisConnection(ctx context.Context) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", "127.0.0.1:6379")
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err = io.WriteString(conn, "*1\r\n$4\r\nPING\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "+PONG\r\n" {
		conn.Close()
		return nil, errors.New("Redis PING 未成功，请检查实例及认证配置")
	}
	return conn, nil
}
func (r *Runtime) Targets(ctx context.Context) (map[int32]int64, error) {
	c, err := kafkaClient(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	parts, err := c.Partitions(topic)
	if err != nil {
		return nil, err
	}
	targets := map[int32]int64{}
	for _, p := range parts {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		targets[p], err = c.GetOffset(topic, p, sarama.OffsetNewest)
		if err != nil {
			return nil, err
		}
	}
	return targets, nil
}
func (r *Runtime) validRun(inst Instance) bool {
	b, err := hex.DecodeString(inst.RunID)
	return err == nil && len(b) == 16 && samePath(inst.RunDir, filepath.Join(r.stateDir, "runs", inst.RunID))
}
func (r *Runtime) Stop(ctx context.Context, id string, inst Instance, targets map[int32]int64) error {
	if !inst.Owned {
		return errors.New("不允许停止原有实例")
	}
	if !r.validRun(inst) {
		return errors.New("本机运行记录不合法，未操作进程")
	}
	if inst.Stopped {
		return nil
	}
	if r.Alive(inst) {
		if err := localproc.Track(inst.Identity); err != nil {
			return err
		}
	}
	if id == "server" || id == "consumer" {
		return r.stopChild(ctx, id, inst, targets)
	}
	// A failed startup can be settled even after launcher recovery, but only
	// with positive exit evidence and no remaining listener. Ready instances
	// retain the stricter successful-shutdown requirement below.
	if !inst.Ready {
		gone, err := localproc.Gone(inst.Identity)
		if err != nil {
			return fmt.Errorf("无法确认初始化进程是否退出：%w", err)
		}
		if gone {
			listeners, err := localproc.Listeners(ports[id])
			if err != nil {
				return err
			}
			if len(listeners) != 0 {
				return errors.New("初始化进程已退出，但端口仍被占用；未操作占用者")
			}
			return nil
		}
	}
	if exited, code, known := localproc.ExitStatus(inst.Identity); known && exited {
		if code == 0 {
			return nil
		}
		return fmt.Errorf("进程异常退出，退出码 %d；未继续停止依赖", code)
	}
	if !r.Alive(inst) {
		return errors.New("进程已消失或身份发生变化，退出结果无法确认")
	}
	// A timeout in initial startup must not be turned into a blind Ctrl+C.
	if !inst.Ready {
		if err := r.nativeReady(ctx, id, inst); err != nil {
			return fmt.Errorf("初始化尚未确认完成，保留进程：%w", err)
		}
	}
	if id == "redis" {
		conn, err := redisConnection(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if err = listenerMatches(id, inst); err != nil {
			return err
		}
		if !r.Alive(inst) {
			return errors.New("Redis 身份变化，未发送停止命令")
		}
		deadline := time.Now().Add(15 * time.Second)
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)
		if _, err = io.WriteString(conn, "*1\r\n$8\r\nSHUTDOWN\r\n"); err != nil {
			return err
		}
		line, readErr := bufio.NewReader(conn).ReadString('\n')
		if line != "" && strings.HasPrefix(line, "-") {
			return errors.New(strings.TrimSpace(line))
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	} else if err := localproc.StopConsole(ctx, inst.Identity); err != nil {
		return err
	}
	return waitExit(ctx, inst)
}
func waitExit(ctx context.Context, inst Instance) error {
	for {
		exited, code, known := localproc.ExitStatus(inst.Identity)
		if known && exited {
			if code != 0 {
				return fmt.Errorf("进程异常退出，退出码 %d", code)
			}
			return nil
		}
		if !known && !localproc.Matches(inst.Identity) {
			return errors.New("无法确认进程的退出结果")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (r *Runtime) stopChild(ctx context.Context, id string, inst Instance, targets map[int32]int64) error {
	s, err := runcontrol.ReadStatus(inst.RunDir, inst.RunID)
	if err == nil && s.PID == inst.Identity.PID && s.Component == id && s.Phase == "stopped" {
		if !r.Alive(inst) {
			return nil
		}
		return waitExit(ctx, inst)
	}
	if !r.Alive(inst) {
		if !inst.Ready && err == nil && s.PID == inst.Identity.PID && s.Component == id && !s.EverReady {
			return nil
		}
		return errors.New("业务进程异常退出或停止结果未确认，依赖已保留")
	}
	if !samePath(inst.Identity.Executable, r.expectedExe(id)) {
		return errors.New("业务程序身份不符")
	}
	if id == "consumer" {
		if err := r.checkGroup(ctx, inst.RunID); err != nil {
			return err
		}
	}
	requestID, err := randomID()
	if err != nil {
		return err
	}
	limit := stopLimits[id]
	if d, ok := ctx.Deadline(); ok {
		limit = time.Until(d) - 100*time.Millisecond
	}
	if limit <= 0 {
		return context.DeadlineExceeded
	}
	if err = runcontrol.WriteRequest(inst.RunDir, inst.RunID, runcontrol.Request{ID: requestID, Action: "stop", Targets: targets, TimeoutMillis: limit.Milliseconds()}); err != nil {
		return err
	}
	for {
		s, err = runcontrol.ReadStatus(inst.RunDir, inst.RunID)
		if err == nil && s.PID == inst.Identity.PID && s.Component == id && s.RequestID == requestID {
			if s.Phase == "stop_failed" {
				return r.safe(errors.New(s.Error))
			}
			if s.Phase == "stopped" {
				return waitExit(ctx, inst)
			}
		}
		if !r.Alive(inst) {
			return errors.New("进程退出但没有匹配的成功停止结果")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// The child can admit work before the launcher's readiness probe finishes.
// Its durable readiness history must therefore also guard replacement after exit.
func settledBusinessExit(id string, inst Instance, s runcontrol.Status, err error) bool {
	return err == nil && s.PID == inst.Identity.PID && s.Component == id && (s.Phase == "stopped" || (!inst.Ready && !s.EverReady))
}
func (r *Runtime) LogTail(id string) (string, error) {
	if _, ok := labels[id]; !ok {
		return "", errors.New("未知组件")
	}
	var parts []string
	for _, suffix := range []string{"stdout", "stderr"} {
		f, err := os.Open(filepath.Join(r.stateDir, "logs", id+"."+suffix+".log"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", err
		}
		if info.Size() > 4096 {
			_, _ = f.Seek(-4096, io.SeekEnd)
		}
		b, err := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		if err != nil {
			return "", err
		}
		parts = append(parts, suffix+":\n"+string(b))
	}
	return r.redact(strings.Join(parts, "\n")), nil
}
func (r *Runtime) TrimLogs() {
	for _, id := range serviceOrder {
		for _, suffix := range []string{"stdout", "stderr"} {
			path := filepath.Join(r.stateDir, "logs", id+"."+suffix+".log")
			info, err := os.Stat(path)
			if err == nil && info.Size() > 1024*1024 {
				_ = os.Truncate(path, 0)
			}
		}
	}
}

// Preflight checks all listening ports before creating any process. Unknown
// business listeners are not adopted even if they return a familiar web page.
func (r *Runtime) Preflight(ctx context.Context, known map[string]Instance) error {
	for _, id := range []string{"server", "mysql", "redis", "kafka"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := localproc.Listeners(ports[id])
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			continue
		}
		old, ok := known[id]
		if ok && len(ids) == 1 && ids[0] == old.Identity && r.Alive(old) {
			continue
		}
		if id == "server" || len(ids) != 1 || !r.configMatches(id, ids[0]) {
			return fmt.Errorf("端口 %d 有未受此配置管理的实例，未启动新的服务", ports[id])
		}
	}
	brokers, err := localproc.Listeners(9092)
	if err != nil {
		return err
	}
	if len(brokers) > 0 {
		allowed := ""
		if consumer, ok := known["consumer"]; ok && consumer.Owned && r.Alive(consumer) {
			allowed = consumer.RunID
		}
		return r.checkGroup(ctx, allowed)
	}
	return nil
}
func (r *Runtime) BeforeStopDependency(ctx context.Context, id string) error {
	servers, err := localproc.Listeners(8080)
	if err != nil {
		return errors.New("无法核对现有弹幕服务，未停止依赖")
	}
	if len(servers) > 0 {
		return errors.New("仍有弹幕服务监听 8080")
	}
	if id == "redis" {
		return nil
	}
	brokers, err := localproc.Listeners(9092)
	if err != nil {
		return errors.New("无法核对 Kafka 实例")
	}
	if len(brokers) == 0 {
		return nil
	}
	if len(brokers) != 1 || !r.configMatches("kafka", brokers[0]) {
		return errors.New("Kafka 实例归属无法确认")
	}
	if err = r.checkGroup(ctx, ""); err != nil {
		return errors.New("仍有消费者或无法确认消费组状态，保留其依赖")
	}
	return nil
}
func (r *Runtime) nativeReady(ctx context.Context, id string, inst Instance) error {
	if err := listenerMatches(id, inst); err != nil {
		return err
	}
	switch id {
	case "mysql":
		return mysqlGreeting(ctx, "127.0.0.1:3306")
	case "kafka":
		c, err := kafkaClient(ctx)
		if err != nil {
			return err
		}
		return c.Close()
	case "redis":
		c, err := redisConnection(ctx)
		if err != nil {
			return err
		}
		return c.Close()
	}
	return errors.New("没有该组件的原生退出机制")
}

// A protocol greeting proves mysqld has begun serving connections without
// requiring the application's schema or SQL SHUTDOWN privileges.
func mysqlGreeting(ctx context.Context, address string) error {
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer c.Close()
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetReadDeadline(deadline)
	var header [5]byte
	if _, err = io.ReadFull(c, header[:]); err != nil {
		return err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if length < 1 || header[3] != 0 || header[4] != 10 {
		return errors.New("未收到有效 MySQL 初始握手")
	}
	return nil
}
