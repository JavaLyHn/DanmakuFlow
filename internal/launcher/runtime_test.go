package launcher

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

func TestGroupOwnershipAndReadiness(t *testing.T) {
	absent := &sarama.GroupDescription{Err: sarama.ErrGroupIDNotFound}
	if err := validateGroup(absent, "", false); err != nil {
		t.Fatal("new group should be allowed", err)
	}
	if err := validateGroup(absent, "owned", true); err == nil {
		t.Fatal("absent group is not ready")
	}
	g := &sarama.GroupDescription{Members: map[string]*sarama.GroupMemberDescription{"m": {ClientId: "foreign"}}}
	if err := validateGroup(g, "owned", true); err == nil {
		t.Fatal("unknown member accepted")
	}
	g.Members["m"].ClientId = "danmakuflow-owned"
	if err := validateGroup(g, "owned", true); err != nil {
		t.Fatal(err)
	}
	if err := validateGroup(g, "", false); err == nil {
		t.Fatal("existing consumer allowed to be duplicated")
	}
}
func TestReplacementRequiresSettledBusinessExit(t *testing.T) {
	inst := Instance{Identity: localproc.Identity{PID: 123}, Owned: true}
	state := runcontrol.Status{PID: 123, Component: "consumer", Phase: "starting"}
	if !settledBusinessExit("consumer", inst, state, nil) {
		t.Fatal("never-admitted startup failure cannot be retried")
	}
	state.EverReady = true
	if settledBusinessExit("consumer", inst, state, nil) {
		t.Fatal("child that admitted work was replaced before launcher readiness was recorded")
	}
	state.Phase = "stopped"
	if !settledBusinessExit("consumer", inst, state, nil) {
		t.Fatal("confirmed successful shutdown cannot be retried")
	}
	state.Component = "server"
	if settledBusinessExit("consumer", inst, state, nil) {
		t.Fatal("wrong component accepted")
	}
	state.Component = "consumer"
	state.PID++
	if settledBusinessExit("consumer", inst, state, nil) {
		t.Fatal("wrong process accepted")
	}
	state.PID = 123
	if settledBusinessExit("consumer", inst, state, errors.New("missing status")) {
		t.Fatal("unknown exit accepted")
	}
}
func TestDiscoverPreservesUnconfirmedChildThatWasReadyBeforeProbe(t *testing.T) {
	dir := t.TempDir()
	runID := "0123456789abcdef0123456789abcdef"
	// Created=0 makes this a definitively non-live identity on every platform.
	old := Instance{Identity: localproc.Identity{PID: 123}, Owned: true, RunID: runID, RunDir: dir}
	state := runcontrol.Status{PID: 123, RunID: runID, Component: "consumer", Phase: "stop_failed", EverReady: true}
	if err := runcontrol.AtomicJSON(filepath.Join(dir, "status.json"), state); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRuntime(Config{}, nil, t.TempDir())
	inst, err := r.Discover(ctx, "consumer", &old)
	if inst != nil || err == nil || !strings.Contains(err.Error(), "上次业务进程异常退出") {
		t.Fatalf("unconfirmed child record bypassed: instance=%v err=%v", inst, err)
	}
}
func TestNativeMySQLReadinessDoesNotRequireApplicationSchema(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte{1, 0, 0, 0, 10})
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		b := make([]byte, 1)
		if n, _ := c.Read(b); n != 0 {
			t.Error("readiness sent database commands")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mysqlGreeting(ctx, listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	<-done
}
