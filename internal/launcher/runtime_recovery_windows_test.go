//go:build windows

package launcher

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
)

// This child never opens a service port or touches a database.
func TestNativeStartupFailureChild(t *testing.T) {
	dir := os.Getenv("DANMAKUFLOW_TEST_NATIVE_EXIT_DIR")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0600); err != nil {
		os.Exit(24)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "exit")); err == nil {
			os.Exit(23)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStopRecoversNeverReadyNativeFailure(t *testing.T) {
	for _, tracked := range []bool{true, false} {
		name := "after-launcher-restart"
		if tracked {
			name = "tracked-exit"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestNativeStartupFailureChild$")
			cmd.Env = append(os.Environ(), "DANMAKUFLOW_TEST_NATIVE_EXIT_DIR="+dir)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			id, err := localproc.Inspect(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			if tracked {
				if err = localproc.Track(id); err != nil {
					t.Fatal(err)
				}
			}
			until := time.Now().Add(10 * time.Second)
			for {
				if _, err = os.Stat(filepath.Join(dir, "ready")); err == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatal("child did not become available")
				}
				time.Sleep(10 * time.Millisecond)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			previousPort := ports["redis"]
			ports["redis"] = listener.Addr().(*net.TCPAddr).Port
			defer func() { ports["redis"] = previousPort }()
			r := NewRuntime(Config{}, nil, dir)
			const runID = "0123456789abcdef0123456789abcdef"
			inst := Instance{Identity: id, Owned: true, RunID: runID, RunDir: filepath.Join(dir, "runs", runID)}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err = r.Stop(ctx, "redis", inst, nil); err == nil || !localproc.Matches(id) {
				t.Fatal("unready living process was discarded or terminated")
			}
			if err = os.WriteFile(filepath.Join(dir, "exit"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 23 {
				t.Fatalf("expected startup failure, got %v", err)
			}
			if err = r.Stop(ctx, "redis", inst, nil); err == nil {
				t.Fatal("failed startup was settled while its port was occupied")
			}
			if err = listener.Close(); err != nil {
				t.Fatal(err)
			}
			inst.Ready = true
			if err = r.Stop(ctx, "redis", inst, nil); err == nil {
				t.Fatal("previously ready process lost its abnormal-exit protection")
			}
			inst.Ready = false
			if err = r.Stop(ctx, "redis", inst, nil); err != nil {
				t.Fatalf("never-ready process is gone and port is free, but cleanup remains blocked: %v", err)
			}
		})
	}
}
