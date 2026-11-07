package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

func dockerFixture(t *testing.T, id string) (*DockerRuntime, Instance, dockerContainer) {
	t.Helper()
	r := &DockerRuntime{Runtime: &Runtime{config: Config{Docker: DockerConfig{Project: "danmakuflow-local", Context: "desktop-linux"}}, stateDir: t.TempDir()}}
	runID := strings.Repeat("a", 32)
	inst := Instance{Owned: true, RunID: runID, RunDir: filepath.Join(r.stateDir, "runs", runID), Container: &ContainerIdentity{ID: strings.Repeat("b", 64), StartedAt: "2026-11-07T01:00:00Z"}}
	if err := os.MkdirAll(inst.RunDir, 0700); err != nil {
		t.Fatal(err)
	}
	c := dockerContainer{ID: inst.Container.ID}
	c.Config.Labels = map[string]string{"com.docker.compose.project": "danmakuflow-local", "com.docker.compose.service": id, dockerConfigLabel: r.ConfigKey(), dockerRunLabel: runID}
	c.State.StartedAt = inst.Container.StartedAt
	c.State.Running = true
	c.State.Status = "running"
	return r, inst, c
}

func TestDockerRejectsReplacedActivation(t *testing.T) {
	for _, change := range []string{"id", "start", "project", "service", "config", "run"} {
		t.Run(change, func(t *testing.T) {
			r, inst, c := dockerFixture(t, "redis")
			switch change {
			case "id":
				c.ID = strings.Repeat("c", 64)
			case "start":
				c.State.StartedAt = "2026-11-07T02:00:00Z"
			case "project":
				c.Config.Labels["com.docker.compose.project"] = "other"
			case "service":
				c.Config.Labels["com.docker.compose.service"] = "mysql"
			case "config":
				c.Config.Labels[dockerConfigLabel] = "other"
			case "run":
				c.Config.Labels[dockerRunLabel] = strings.Repeat("d", 32)
			}
			if err := r.verify("redis", inst, c); err == nil {
				t.Fatal("unverified container accepted")
			}
		})
	}
}

func TestDockerStopNeverSignalsExternalOrUninspectableContainer(t *testing.T) {
	for _, external := range []bool{true, false} {
		r, inst, _ := dockerFixture(t, "redis")
		inst.Owned = !external
		calls := 0
		r.run = func(_ context.Context, args ...string) ([]byte, error) {
			calls++
			if len(args) == 0 || args[0] != "inspect" {
				t.Fatalf("mutation on unverified container: %v", args)
			}
			return nil, errors.New("daemon unavailable")
		}
		if err := r.Stop(context.Background(), "redis", inst, nil); err == nil {
			t.Fatal("unknown/external stop accepted")
		}
		if external && calls != 0 {
			t.Fatal("external instance was touched")
		}
	}
}

func TestDockerStopTimeoutDoesNotEscalateSignal(t *testing.T) {
	r, inst, c := dockerFixture(t, "redis")
	inst.Ready = true
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
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Stop(ctx, "redis", inst, nil); err == nil {
		t.Fatal("running container reported stopped")
	}
	if signals != 1 {
		t.Fatalf("signals=%d", signals)
	}
}

func TestDockerBusinessExitNeedsMatchingStatusAndCleanExit(t *testing.T) {
	for _, condition := range []string{"valid", "pid", "component", "readyCrash", "exit", "oom"} {
		t.Run(condition, func(t *testing.T) {
			r, inst, c := dockerFixture(t, "server")
			inst.Ready = true
			c.State.Running = false
			c.State.Status = "exited"
			c.State.FinishedAt = "2026-11-07T01:01:00Z"
			status := runcontrol.Status{PID: 1, RunID: inst.RunID, Component: "server", Phase: "stopped", EverReady: true}
			switch condition {
			case "pid":
				status.PID = 2
			case "component":
				status.Component = "consumer"
			case "readyCrash":
				status.Phase = "ready"
			case "exit":
				c.State.ExitCode = 1
			case "oom":
				c.State.OOMKilled = true
			}
			if err := runcontrol.AtomicJSON(filepath.Join(inst.RunDir, "status.json"), status); err != nil {
				t.Fatal(err)
			}
			r.run = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] != "inspect" {
					t.Fatalf("unexpected mutation %v", args)
				}
				return json.Marshal([]dockerContainer{c})
			}
			err := r.Stop(context.Background(), "server", inst, nil)
			if (err == nil) != (condition == "valid") {
				t.Fatalf("condition=%s err=%v", condition, err)
			}
		})
	}
}

func TestDockerDiscoverRecoversOwnedReceiptAndProtectsExternal(t *testing.T) {
	for _, hasReceipt := range []bool{false, true} {
		r, inst, c := dockerFixture(t, "redis")
		if hasReceipt {
			if err := r.receipt(inst); err != nil {
				t.Fatal(err)
			}
		}
		r.run = func(_ context.Context, args ...string) ([]byte, error) {
			switch args[0] {
			case "ps":
				return []byte(c.ID + "\n"), nil
			case "inspect":
				return json.Marshal([]dockerContainer{c})
			default:
				t.Fatalf("discovery mutated state: %v", args)
				return nil, nil
			}
		}
		got, err := r.Discover(context.Background(), "redis", nil)
		if err != nil || got == nil || got.Owned != hasReceipt {
			t.Fatalf("receipt=%v got=%+v err=%v", hasReceipt, got, err)
		}
	}
}

func TestDockerRecoverCreatedIntentCannotClaimRunningContainer(t *testing.T) {
	for _, running := range []bool{false, true} {
		r, inst, c := dockerFixture(t, "redis")
		c.State.Running = running
		if !running {
			c.State.Status = "created"
		}
		if err := runcontrol.AtomicJSON(filepath.Join(inst.RunDir, "intent.json"), dockerIntent{"redis", r.ConfigKey(), inst.RunID}); err != nil {
			t.Fatal(err)
		}
		r.run = func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "ps" {
				return []byte(c.ID), nil
			}
			if args[0] == "inspect" {
				return json.Marshal([]dockerContainer{c})
			}
			t.Fatalf("unexpected %v", args)
			return nil, nil
		}
		got, err := r.Discover(context.Background(), "redis", nil)
		if err != nil || got == nil || got.Owned == running {
			t.Fatalf("running=%v got=%+v err=%v", running, got, err)
		}
	}
}

func TestDockerRecoveryUsesVerifiedNewerReceipt(t *testing.T) {
	r, inst, c := dockerFixture(t, "redis")
	old := inst
	old.Container = &ContainerIdentity{ID: c.ID, StartedAt: "0001-01-01T00:00:00Z"}
	if err := r.receipt(inst); err != nil {
		t.Fatal(err)
	}
	r.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte(c.ID), nil
		}
		return json.Marshal([]dockerContainer{c})
	}
	got, err := r.Discover(context.Background(), "redis", &old)
	if err != nil || got == nil || got.Container.StartedAt != c.State.StartedAt {
		t.Fatalf("newer activation receipt ignored: got=%+v err=%v", got, err)
	}
	// A later external restart must still not be adopted.
	c.State.StartedAt = "2026-11-07T03:00:00Z"
	if _, err = r.Discover(context.Background(), "redis", &old); err == nil {
		t.Fatal("external restart adopted")
	}
}

func TestDockerConsumerRefusesMissingDrainTargets(t *testing.T) {
	r, inst, c := dockerFixture(t, "consumer")
	inst.Ready = true
	if err := runcontrol.AtomicJSON(filepath.Join(inst.RunDir, "status.json"), runcontrol.Status{PID: 1, RunID: inst.RunID, Component: "consumer", Phase: "ready", EverReady: true}); err != nil {
		t.Fatal(err)
	}
	r.run = func(_ context.Context, args ...string) ([]byte, error) { return json.Marshal([]dockerContainer{c}) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := r.Stop(ctx, "consumer", inst, nil)
	if err == nil || !strings.Contains(err.Error(), "停止目标") {
		t.Fatalf("missing targets did not fail closed before network/drain: %v", err)
	}
	if _, err = os.Stat(filepath.Join(inst.RunDir, "request.json")); !os.IsNotExist(err) {
		t.Fatal("consumer stop request sent without drain targets")
	}
}

func TestDockerFailedInitializationCanBeReplacedAfterStop(t *testing.T) {
	r, inst, c := dockerFixture(t, "mysql")
	c.State.Running = false
	c.State.Status = "exited"
	c.State.ExitCode = 1
	r.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte(c.ID), nil
		}
		return json.Marshal([]dockerContainer{c})
	}
	if err := r.Stop(context.Background(), "mysql", inst, nil); err != nil {
		t.Fatal(err)
	}
	inst.Stopped = true
	got, err := r.Discover(context.Background(), "mysql", &inst)
	if err != nil || got != nil {
		t.Fatalf("settled failed-start container still blocks retry: %v %v", got, err)
	}
}

func TestDockerKafkaControlledTermExit(t *testing.T) {
	for _, tc := range []struct {
		id                           string
		controlled, oom, wantSuccess bool
	}{
		{"kafka", false, false, false}, {"kafka", true, false, true}, {"kafka", true, true, false}, {"redis", true, false, false},
	} {
		r, inst, c := dockerFixture(t, tc.id)
		inst.Ready = true
		c.State.Running = tc.controlled
		c.State.OOMKilled = tc.oom
		c.State.Status = "exited"
		c.State.ExitCode = 143
		if tc.controlled {
			c.State.Status = "running"
		}
		r.run = func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "kill" {
				if strings.Join(args, " ") != "kill --signal=SIGTERM "+c.ID {
					t.Fatalf("unexpected %v", args)
				}
				c.State.Running = false
				c.State.Status = "exited"
				return nil, nil
			}
			return json.Marshal([]dockerContainer{c})
		}
		err := r.Stop(context.Background(), tc.id, inst, nil)
		if (err == nil) != tc.wantSuccess {
			t.Fatalf("case=%+v err=%v", tc, err)
		}
	}
}
