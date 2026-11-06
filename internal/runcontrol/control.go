// Package runcontrol provides an optional private, cooperative child lifecycle.
// It never starts or kills a process. Files live in a launcher-owned private directory.
package runcontrol

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Request struct {
	ID            string          `json:"id"`
	Action        string          `json:"action"`
	Targets       map[int32]int64 `json:"targets,omitempty"`
	TimeoutMillis int64           `json:"timeoutMillis"`
	RunID         string          `json:"runId"`
}
type Status struct {
	PID       int    `json:"pid"`
	RunID     string `json:"runId"`
	Component string `json:"component"`
	Phase     string `json:"phase"`
	Error     string `json:"error,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	EverReady bool   `json:"everReady"`
}
type Child struct {
	mu       sync.Mutex
	dir      string
	state    Status
	writeErr error
}

func validID(id string) bool { b, err := hex.DecodeString(id); return err == nil && len(b) == 16 }

func FromEnv(component string) (*Child, error) {
	dir, id := os.Getenv("DANMAKUFLOW_RUN_DIR"), os.Getenv("DANMAKUFLOW_RUN_ID")
	if dir == "" && id == "" {
		return nil, nil
	}
	if !filepath.IsAbs(dir) || !validID(id) || (component != "server" && component != "consumer") {
		return nil, errors.New("invalid local lifecycle configuration")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("local lifecycle directory unavailable")
	}
	c := &Child{dir: dir, state: Status{PID: os.Getpid(), RunID: id, Component: component, Phase: "starting"}}
	if err := AtomicJSON(filepath.Join(dir, "status.json"), c.state); err != nil {
		return nil, fmt.Errorf("write lifecycle state: %w", err)
	}
	return c, nil
}

// Report is a no-op for unmanaged command-line processes. Readiness cannot
// resurrect a child whose stop operation has already begun or failed.
func (c *Child) Report(phase string, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if (phase == "ready" || phase == "starting") && (c.state.Phase == "stopping" || c.state.Phase == "stop_failed" || c.state.Phase == "stopped") {
		return
	}
	c.state.Phase = phase
	if phase == "ready" {
		c.state.EverReady = true
	}
	c.state.Error = ""
	if err != nil {
		c.state.Error = err.Error()
	}
	c.writeErr = AtomicJSON(filepath.Join(c.dir, "status.json"), c.state)
}

// Err lets a managed child reject admission when its readiness cannot be saved.
func (c *Child) Err() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeErr
}

// Serve returns nil only after a successful stop and durable local result. A
// canceled parent context stops the watcher, not the business service itself.
func (c *Child) Serve(ctx context.Context, stop func(context.Context, Request) error) error {
	if c == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		var req Request
		if err := ReadJSON(filepath.Join(c.dir, "request.json"), &req); err != nil || validateRequest(req) != nil || req.RunID != c.state.RunID || req.ID == last {
			continue
		}
		last = req.ID
		c.mu.Lock()
		c.state.RequestID = req.ID
		c.mu.Unlock()
		c.Report("stopping", nil)
		c.mu.Lock()
		err := c.writeErr
		c.mu.Unlock()
		if err != nil {
			return err
		}
		limit := time.Duration(req.TimeoutMillis) * time.Millisecond
		if limit == 0 {
			limit = 30 * time.Second
		}
		attempt, cancel := context.WithTimeout(ctx, limit)
		err = stop(attempt, req)
		cancel()
		if err != nil {
			c.Report("stop_failed", err)
			continue
		}
		c.Report("stopped", nil)
		c.mu.Lock()
		err = c.writeErr
		c.mu.Unlock()
		return err
	}
}

func validateRequest(r Request) error {
	if !validID(r.RunID) || r.Action != "stop" || strings.TrimSpace(r.ID) == "" || len(r.ID) > 128 || r.TimeoutMillis < 0 || r.TimeoutMillis > 120000 || len(r.Targets) > 4096 {
		return errors.New("invalid lifecycle request")
	}
	for p, n := range r.Targets {
		if p < 0 || n < 0 {
			return errors.New("invalid partition target")
		}
	}
	return nil
}
func WriteRequest(dir, id string, r Request) error {
	r.RunID = id
	if err := validateRequest(r); err != nil {
		return err
	}
	return AtomicJSON(filepath.Join(dir, "request.json"), r)
}
func ReadStatus(dir, id string) (Status, error) {
	var s Status
	err := ReadJSON(filepath.Join(dir, "status.json"), &s)
	if err != nil {
		return s, err
	}
	if s.RunID != id || !validID(id) || s.PID <= 0 {
		return Status{}, errors.New("lifecycle identity mismatch")
	}
	return s, nil
}

// ReadJSON bounds local input and rejects trailing data and unknown fields.
func ReadJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return errors.New("invalid local JSON file")
	}
	dec := json.NewDecoder(io.LimitReader(f, 64*1024+1))
	dec.DisallowUnknownFields()
	if err = dec.Decode(value); err != nil {
		return err
	}
	var tail any
	if err = dec.Decode(&tail); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func AtomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 64*1024 {
		return errors.New("local JSON too large")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		err = os.Rename(name, path)
		if err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}
