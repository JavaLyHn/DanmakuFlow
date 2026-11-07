package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)

func TestInitializeRecoversCreatedJobBeforeRefusingExistingOwnedServices(t *testing.T) {
	r, mysql, c := dockerFixture(t, "mysql")
	r.config.Version = 1
	r.config.Backend = "docker"
	r.config.RepoDir = t.TempDir()
	r.dsn = "root:fixture@tcp(127.0.0.1:3306)/danmakuflow"
	for name, target := range map[string]*string{"docker.exe": &r.config.Docker.Executable, "compose.yaml": &r.config.Docker.ComposeFile, "docker.env": &r.config.Docker.EnvFile} {
		*target = filepath.Join(r.config.RepoDir, name)
		if err := os.WriteFile(*target, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c.Config.Labels[dockerConfigLabel] = r.ConfigKey()
	runID := strings.Repeat("d", 32)
	jobDir := filepath.Join(r.stateDir, "runs", runID)
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runcontrol.AtomicJSON(filepath.Join(jobDir, "intent.json"), dockerIntent{"migrate", r.ConfigKey(), runID}); err != nil {
		t.Fatal(err)
	}
	job := dockerContainer{ID: strings.Repeat("c", 64)}
	job.Config.Labels = map[string]string{"com.docker.compose.project": r.config.Docker.Project, "com.docker.compose.service": "migrate", dockerConfigLabel: r.ConfigKey(), dockerRunLabel: runID}
	job.State.Status = "created"
	job.State.StartedAt = "0001-01-01T00:00:00Z"
	path := filepath.Join(r.stateDir, "instances.json")
	if err := runcontrol.AtomicJSON(path, savedState{ConfigKey: r.ConfigKey(), Instances: map[string]Instance{"mysql": mysql}}); err != nil {
		t.Fatal(err)
	}
	r.run = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "service=mysql") {
				return []byte(c.ID), nil
			}
			if strings.Contains(joined, "service=migrate") {
				return []byte(job.ID), nil
			}
			return nil, nil
		case "inspect":
			if args[len(args)-1] == job.ID {
				return json.Marshal([]dockerContainer{job})
			}
			return json.Marshal([]dockerContainer{c})
		default:
			t.Fatalf("recovery performed mutation: %v", args)
			return nil, nil
		}
	}
	if err := r.InitializeDocker(context.Background(), path); err == nil {
		t.Fatal("existing owned service should block initialization")
	}
	var state savedState
	if err := runcontrol.ReadJSON(path, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Instances["migrate"].Owned || state.Instances["migrate"].Container == nil {
		t.Fatal("created orphan job was not recovered for page stop")
	}
}

func TestSetupRejectsForeignConsumerBeforeCreatingJobs(t *testing.T) {
	for _, id := range []string{"migrate", "kafka-init"} {
		r, _, _ := dockerFixture(t, id)
		r.run = func(_ context.Context, args ...string) ([]byte, error) {
			t.Fatalf("job inspected or created before idle group check: %v", args)
			return nil, nil
		}
		foreign := errors.New("foreign consumer is running")
		d := &dockerSetupDriver{DockerRuntime: r, checkIdle: func(context.Context) error { return foreign }}
		if _, err := d.Discover(context.Background(), id, nil); !errors.Is(err, foreign) {
			t.Fatalf("external consumer was not rejected: %v", err)
		}
	}
}

func TestSetupLifecycleRecordsJobsAndPreservesReusedInfrastructure(t *testing.T) {
	f := newFake()
	f.live["mysql"] = true
	path := filepath.Join(t.TempDir(), "instances.json")
	m, err := NewManager(f, path, "setup-test")
	if err != nil {
		t.Fatal(err)
	}
	if err = runDockerInitialization(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:redis", "start:kafka", "start:migrate", "start:kafka-init", "stop:migrate", "stop:kafka-init", "stop:kafka", "stop:redis"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events=%v, want=%v", f.events, want)
	}
	var saved savedState
	if err = runcontrol.ReadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"redis", "kafka", "migrate", "kafka-init"} {
		if !saved.Instances[id].Owned || !saved.Instances[id].Stopped {
			t.Fatalf("%s lifecycle not saved: %+v", id, saved.Instances[id])
		}
	}
	if !f.live["mysql"] || saved.Instances["mysql"].Owned || m.Snapshot().Phase != "partial" {
		t.Fatal("reused MySQL was not retained")
	}
}

type failingSetupProbe struct{ *fakeDriver }

func (f *failingSetupProbe) Probe(_ context.Context, id string, _ Instance) error {
	if id == "migrate" {
		return errors.New("migration failed")
	}
	return nil
}

func TestFailedSetupLeavesRecordedJobAndDependenciesForPageStop(t *testing.T) {
	f := &failingSetupProbe{newFake()}
	path := filepath.Join(t.TempDir(), "instances.json")
	m, err := NewManager(f, path, "setup-test")
	if err != nil {
		t.Fatal(err)
	}
	if err = runDockerInitialization(context.Background(), m); err == nil {
		t.Fatal("failed migration reported successful initialization")
	}
	var saved savedState
	if err = runcontrol.ReadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"mysql", "redis", "kafka", "migrate"} {
		if !saved.Instances[id].Owned || saved.Instances[id].Stopped || !f.live[id] {
			t.Fatalf("failed setup lost %s", id)
		}
	}
	if _, ok := saved.Instances["kafka-init"]; ok {
		t.Fatal("second job started after migration failed")
	}
	reloaded, err := NewManager(f, path, "setup-test")
	if err != nil || !reloaded.Snapshot().CanStop {
		t.Fatalf("page cannot recover failed setup: %v", err)
	}
	if err = reloaded.begin("stop"); err != nil {
		t.Fatal(err)
	}
	reloaded.stop()
	if len(f.live) != 0 {
		t.Fatalf("page stop left services running: %v", f.live)
	}
}

func TestSetupRecoveryPersistsReceiptAndRejectsPreviousOwnership(t *testing.T) {
	r, inst, c := dockerFixture(t, "redis")
	if err := r.receipt(inst); err != nil {
		t.Fatal(err)
	}
	r.run = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			if strings.Contains(strings.Join(args, " "), "label=com.docker.compose.service=redis") {
				return []byte(c.ID), nil
			}
			return nil, nil
		case "inspect":
			return json.Marshal([]dockerContainer{c})
		default:
			t.Fatalf("recovery changed a container: %v", args)
			return nil, nil
		}
	}
	path := filepath.Join(t.TempDir(), "instances.json")
	m, err := NewManager(r, path, r.ConfigKey())
	if err != nil {
		t.Fatal(err)
	}
	if err = r.recoverSetupInstances(context.Background(), m); err == nil {
		t.Fatal("previous owned infrastructure accepted for automatic cleanup")
	}
	var saved savedState
	if err = runcontrol.ReadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Instances["redis"].Container.ID != c.ID || !saved.Instances["redis"].Owned {
		t.Fatal("recovered receipt was not saved for page stop")
	}
}
