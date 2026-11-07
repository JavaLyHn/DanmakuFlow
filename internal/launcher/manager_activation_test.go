package launcher

import (
	"context"
	"errors"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
	"path/filepath"
	"strings"
	"testing"
)

type partialCreateDriver struct{ *fakeDriver }

func (f *partialCreateDriver) Start(ctx context.Context, id string) (Instance, error) {
	inst, err := f.fakeDriver.Start(ctx, id)
	if id == "mysql" && err == nil {
		return inst, errors.New("created but activation failed")
	}
	return inst, err
}
func TestManagerRecordsPartialCreation(t *testing.T) {
	f := &partialCreateDriver{newFake()}
	path := filepath.Join(t.TempDir(), "instances.json")
	m, _ := NewManager(f, path, "docker-test")
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	waitOperation(t, m)
	if m.Snapshot().Error == "" || !m.Snapshot().CanStop {
		t.Fatal("partial creation lost")
	}
	var saved savedState
	if err := runcontrol.ReadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Instances["mysql"].Owned {
		t.Fatal("partial creation not durably recorded")
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitOperation(t, m)
	if f.live["mysql"] {
		t.Fatal("partial container not stopped")
	}
}

type activationDriver struct {
	*fakeDriver
	stateFile string
	observed  bool
}

func (f *activationDriver) Activate(_ context.Context, id string, inst *Instance) error {
	var saved savedState
	if err := runcontrol.ReadJSON(f.stateFile, &saved); err != nil {
		return err
	}
	if !saved.Instances[id].Owned {
		return errors.New("container activated before ownership was saved")
	}
	f.observed = true
	inst.Container = &ContainerIdentity{ID: strings.Repeat("b", 64), StartedAt: "2026-11-07T01:00:00Z"}
	return errors.New("start acknowledgement timed out")
}
func TestManagerPersistsBeforeAndAfterFailedActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f := &activationDriver{fakeDriver: newFake(), stateFile: path}
	m, _ := NewManager(f, path, "docker-test")
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	waitOperation(t, m)
	if !f.observed || m.Snapshot().Error == "" {
		t.Fatal("activation not tracked")
	}
	var saved savedState
	if err := runcontrol.ReadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Instances["mysql"].Container == nil || saved.Instances["mysql"].Container.StartedAt == "" {
		t.Fatal("activation identity lost on error")
	}
}
