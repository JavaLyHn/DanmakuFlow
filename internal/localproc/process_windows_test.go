//go:build windows

package localproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "-console-stop" {
		if err := ConsoleHelper(os.Args[2]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestFakeProcess is only entered in processes explicitly created by these tests.
func TestFakeProcess(t *testing.T) {
	dir := os.Getenv("LOCALPROC_TEST_CHILD")
	if dir == "" {
		return
	}
	if os.Getenv("LOCALPROC_TEST_EXTRA") == "1" {
		_ = os.WriteFile(filepath.Join(dir, "extra"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			time.Sleep(time.Hour)
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	if os.Getenv("LOCALPROC_TEST_SHARED") == "1" {
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		cmd := exec.Command(exe, "-test.run=^TestFakeProcess$")
		cmd.Env = append(os.Environ(), "LOCALPROC_TEST_EXTRA=1")
		if err = cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = cmd.Process.Release()
	}
	_ = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600)
	if os.Getenv("LOCALPROC_TEST_SELF_EXIT") == "1" {
		for {
			if _, err := os.Stat(filepath.Join(dir, "exit")); err == nil {
				os.Exit(23)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	<-signals
	_ = os.WriteFile(filepath.Join(dir, "interrupt"), []byte("received"), 0600)
	if os.Getenv("LOCALPROC_TEST_DELAY") == "1" {
		time.Sleep(5 * time.Second)
	}
	if os.Getenv("LOCALPROC_TEST_FAIL") == "1" {
		os.Exit(23)
	}
}
func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if b, err := os.ReadFile(path); err == nil {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}
func testChild(t *testing.T, shared bool) (Identity, string) { return testChildMode(t, shared, "") }
func testChildMode(t *testing.T, shared bool, mode string) (Identity, string) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LOCALPROC_TEST_CHILD="+dir)
	if shared {
		env = append(env, "LOCALPROC_TEST_SHARED=1")
	}
	if mode != "" {
		env = append(env, mode+"=1")
	}
	id, err := Start(StartOptions{Executable: exe, Args: []string{"-test.run=^TestFakeProcess$"}, Env: env, Console: true})
	if err != nil {
		t.Fatal(err)
	}
	var extra Identity
	// Cleanup applies exclusively to children created by this test, even on failure.
	t.Cleanup(func() {
		if Matches(extra) {
			if p, e := os.FindProcess(extra.PID); e == nil {
				_ = p.Kill()
			}
		}
		if Matches(id) {
			if p, e := os.FindProcess(id.PID); e == nil {
				_ = p.Kill()
			}
		}
	})
	waitFile(t, filepath.Join(dir, "ready"))
	if shared {
		b := waitFile(t, filepath.Join(dir, "extra"))
		pid, e := strconv.Atoi(string(b))
		if e != nil {
			t.Fatal(e)
		}
		extra, e = Inspect(pid)
		if e != nil {
			t.Fatal(e)
		}
	}
	return id, dir
}
func TestStopConsoleSignalsIsolatedChild(t *testing.T) {
	id, dir := testChild(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := StopConsole(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(dir, "interrupt"))
}
func TestStopConsoleRefusesSharedConsole(t *testing.T) {
	id, dir := testChild(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := StopConsole(ctx, id); err == nil {
		t.Fatal("accepted shared console")
	}
	if !Matches(id) {
		t.Fatal("shared child terminated")
	}
	if _, err := os.Stat(filepath.Join(dir, "interrupt")); err == nil {
		t.Fatal("signalled shared child")
	}
}
func TestStopConsoleTimeoutLeavesChildRunning(t *testing.T) {
	id, dir := testChild(t, false)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := StopConsole(ctx, id); err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
	if !Matches(id) {
		t.Fatal("cancelled stop terminated child")
	}
	if _, err := os.Stat(filepath.Join(dir, "interrupt")); err == nil {
		t.Fatal("cancelled stop signalled child")
	}
}
func TestProcessArgsReadsChildArguments(t *testing.T) {
	id, _ := testChild(t, false)
	args, err := ProcessArgs(id.PID)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 2 || !strings.Contains(strings.Join(args, " "), "-test.run=^TestFakeProcess$") {
		t.Fatal(fmt.Errorf("expected test argument in process command line"))
	}
}

func TestStopConsoleReportsNonzeroExit(t *testing.T) {
	id, _ := testChildMode(t, false, "LOCALPROC_TEST_FAIL")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := StopConsole(ctx, id); err == nil {
		t.Fatal("nonzero exit reported as successful")
	}
	exited, code, known := ExitStatus(id)
	if !known || !exited || code != 23 {
		t.Fatalf("exit status: %v %d %v", exited, code, known)
	}
}
func TestStopConsoleWaitTimeoutKeepsTargetTracked(t *testing.T) {
	id, dir := testChildMode(t, false, "LOCALPROC_TEST_DELAY")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := StopConsole(ctx, id); err != context.DeadlineExceeded {
		t.Fatalf("expected timeout, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "interrupt")); err != nil {
		t.Fatalf("target did not receive interrupt before timeout: %v", err)
	}
	exited, _, known := ExitStatus(id)
	if !known || exited || !Matches(id) {
		t.Fatal("timed-out child is not tracked and running")
	}
}
func TestStopConsoleCachesCleanExit(t *testing.T) {
	id, _ := testChild(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := StopConsole(ctx, id); err != nil {
		t.Fatal(err)
	}
	exited, code, known := ExitStatus(id)
	if !known || !exited || code != 0 {
		t.Fatalf("exit status: %v %d %v", exited, code, known)
	}
}

func TestExitedChildDoesNotMatchWhileHandleRetained(t *testing.T) {
	id, dir := testChildMode(t, false, "LOCALPROC_TEST_SELF_EXIT")
	trackedMu.Lock()
	handle := tracked[id].handle
	trackedMu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "exit"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Do not call ExitStatus yet: the retained handle deliberately keeps the
	// terminated Windows process object available for identity queries.
	status, _, err := waitSingle.Call(handle, 10000)
	if status != 0 {
		t.Fatalf("child did not exit: status=%d error=%v", status, err)
	}
	if Matches(id) {
		t.Error("exited child still considered alive")
	}
	if _, err := Inspect(id.PID); err == nil {
		t.Error("Inspect accepted terminated process object")
	}
	exited, code, known := ExitStatus(id)
	if !known || !exited || code != 23 {
		t.Fatalf("native exit evidence lost: %v %d %v", exited, code, known)
	}
}
