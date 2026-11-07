package launcher

import (
	"context"
	"encoding/json"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupExitSeparatesSuccessfulInitializationFromSettledFailure(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		code         int
		oom, success bool
		wantError    bool
	}{
		{"success", "exited", 0, false, true, false},
		{"failed job", "exited", 1, false, true, true},
		{"oom", "exited", 0, true, true, true},
		{"never started", "created", 0, false, true, true},
		{"settled failure", "exited", 1, false, false, false},
		{"settled oom", "exited", 137, true, false, false},
		{"cancelled before activation", "created", 0, false, false, false},
		{"unknown terminal state", "dead", 0, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, inst, c := dockerFixture(t, "migrate")
			c.State.Running = false
			c.State.Status, c.State.ExitCode, c.State.OOMKilled = tc.status, tc.code, tc.oom
			r.run = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] != "inspect" {
					t.Fatalf("wait mutated container: %v", args)
				}
				return json.Marshal([]dockerContainer{c})
			}
			err := r.waitSetupExit(context.Background(), "migrate", inst, tc.success)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, want error=%v", err, tc.wantError)
			}
		})
	}
}

func TestSetupWaitRejectsIdentityChangeAfterPolling(t *testing.T) {
	r, inst, c := dockerFixture(t, "kafka-init")
	reads := 0
	r.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "inspect" {
			t.Fatalf("unexpected command: %v", args)
		}
		reads++
		if reads > 1 {
			c.State.Running, c.State.Status = false, "exited"
			c.Config.Labels[dockerRunLabel] = strings.Repeat("c", 32)
		}
		return json.Marshal([]dockerContainer{c})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.waitSetupJob(ctx, "kafka-init", inst); err == nil || reads != 2 {
		t.Fatalf("changed identity accepted: reads=%d err=%v", reads, err)
	}
}

func TestSetupStopOnlySendsTermAndRetainsTimedOutJob(t *testing.T) {
	r, inst, c := dockerFixture(t, "migrate")
	signals := 0
	r.run = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			return json.Marshal([]dockerContainer{c})
		case "kill":
			if strings.Join(args, " ") != "kill --signal=SIGTERM "+inst.Container.ID {
				t.Fatalf("unsafe signal: %v", args)
			}
			signals++
			return nil, nil
		default:
			t.Fatalf("unexpected mutation: %v", args)
			return nil, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Stop(ctx, "migrate", inst, nil); err == nil || signals != 1 {
		t.Fatalf("timeout was reported as stopped: signals=%d err=%v", signals, err)
	}
	var receipt Instance
	if runcontrol.ReadJSON(filepath.Join(inst.RunDir, "container.json"), &receipt) == nil && receipt.Stopped {
		t.Fatal("timed-out job was recorded as stopped")
	}
}
