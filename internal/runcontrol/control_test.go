package runcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func childForTest(t *testing.T) (*Child, string, string) {
	t.Helper()
	dir, id := t.TempDir(), strings.Repeat("a", 32)
	t.Setenv("DANMAKUFLOW_RUN_DIR", dir)
	t.Setenv("DANMAKUFLOW_RUN_ID", id)
	c, err := FromEnv("server")
	if err != nil {
		t.Fatal(err)
	}
	return c, dir, id
}

func waitState(t *testing.T, dir, id, phase string) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, err := ReadStatus(dir, id)
		if err == nil && s.Phase == phase {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, err := ReadStatus(dir, id)
	t.Fatalf("want %s, got %+v (%v)", phase, s, err)
	return Status{}
}

func TestControlDisabledAndPartialEnvironment(t *testing.T) {
	t.Setenv("DANMAKUFLOW_RUN_DIR", "")
	t.Setenv("DANMAKUFLOW_RUN_ID", "")
	c, err := FromEnv("server")
	if c != nil || err != nil {
		t.Fatalf("unexpected enabled control: %v", err)
	}
	t.Setenv("DANMAKUFLOW_RUN_ID", strings.Repeat("a", 32))
	if _, err = FromEnv("server"); err == nil {
		t.Fatal("partial config accepted")
	}
}
func TestAdmissionHistorySurvivesLossOfReadiness(t *testing.T) {
	c, dir, id := childForTest(t)
	c.Report("ready", nil)
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	c.Report("starting", nil)
	s, err := ReadStatus(dir, id)
	if err != nil || !s.EverReady {
		t.Fatal("prior admission history lost")
	}
}

func TestFailedRequestIsNotRepeatedAndNewRequestCanRetry(t *testing.T) {
	c, dir, id := childForTest(t)
	c.Report("ready", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- c.Serve(ctx, func(context.Context, Request) error {
			if calls.Add(1) == 1 {
				return errors.New("flush failed")
			}
			return nil
		})
	}()
	if err := WriteRequest(dir, id, Request{ID: "one", Action: "stop", TimeoutMillis: 1000}); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, dir, id, "stop_failed")
	if s.Error != "flush failed" {
		t.Fatalf("error absent: %+v", s)
	}
	c.Report("ready", nil)
	time.Sleep(450 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("same request executed twice")
	}
	if s, _ := ReadStatus(dir, id); s.Phase != "stop_failed" {
		t.Fatal("readiness overwrote failed stop")
	}
	if err := WriteRequest(dir, id, Request{ID: "two", Action: "stop", TimeoutMillis: 1000}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry stalled")
	}
	s = waitState(t, dir, id, "stopped")
	if s.RequestID != "two" || s.PID != os.Getpid() {
		t.Fatalf("incorrect result: %+v", s)
	}
}

func TestForeignRunAndMalformedRequestsNeverStopChild(t *testing.T) {
	c, dir, id := childForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx, func(context.Context, Request) error { calls.Add(1); return nil }) }()
	if err := WriteRequest(dir, strings.Repeat("b", 32), Request{ID: "foreign", Action: "stop", TimeoutMillis: 1000}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"runId":"`+id+`","id":"invalid","action":"exec"}`), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if calls.Load() != 0 {
		t.Fatal("foreign/malformed request executed")
	}
	if _, err := ReadStatus(dir, strings.Repeat("b", 32)); err == nil {
		t.Fatal("foreign status accepted")
	}
}

func TestStopDeadlineAndCancellationDoNotReportSuccess(t *testing.T) {
	c, dir, id := childForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Serve(ctx, func(ctx context.Context, _ Request) error { <-ctx.Done(); return ctx.Err() })
	}()
	if err := WriteRequest(dir, id, Request{ID: "deadline", Action: "stop", TimeoutMillis: 20}); err != nil {
		t.Fatal(err)
	}
	waitState(t, dir, id, "stop_failed")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if err := WriteRequest(dir, id, Request{ID: "bad", Action: "stop", Targets: map[int32]int64{-1: 2}}); err == nil {
		t.Fatal("invalid partition accepted")
	}
}
