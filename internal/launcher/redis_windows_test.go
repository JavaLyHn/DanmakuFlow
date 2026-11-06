//go:build windows

package launcher

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
)

// Explicit opt-in: ordinary unit tests never start a real dependency.
// A native drive path with spaces must work with the MSYS Redis build too.
func TestRedisLoadsWindowsConfigPath(t *testing.T) {
	exe := os.Getenv("DANMAKUFLOW_TEST_REDIS_EXE")
	if exe == "" {
		t.Skip("real Redis smoke test requires DANMAKUFLOW_TEST_REDIS_EXE")
	}
	dir := filepath.Join(t.TempDir(), "config with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	config := filepath.Join(dir, "redis.conf")
	contents := fmt.Sprintf("bind 127.0.0.1\nport %d\nsave \"\"\nappendonly no\nmaxmemory 16mb\nio-threads 1\nlogfile \"\"\n", port)
	if err = os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(Config{RepoDir: dir, Redis: RedisConfig{Executable: exe, ConfigFile: config, WorkingDir: filepath.Dir(exe)}}, nil, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inst, err := r.Start(ctx, "redis")
	if err != nil {
		t.Fatal(err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	t.Cleanup(func() {
		if !r.Alive(inst) {
			return
		}
		ids, e := localproc.Listeners(port)
		if e != nil || len(ids) != 1 || ids[0] != inst.Identity {
			t.Error("cannot safely stop Redis: listener identity differs", e)
			return
		}
		conn, e := net.DialTimeout("tcp", address, time.Second)
		if e != nil {
			t.Error(e)
			return
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = fmt.Fprint(conn, "*1\r\n$8\r\nSHUTDOWN\r\n")
		_ = conn.Close()
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if e = waitExit(stop, inst); e != nil {
			t.Error(e)
		}
	})
	for {
		if !r.Alive(inst) {
			logs, _ := r.LogTail("redis")
			t.Fatalf("Redis exited before loading the Windows config path: %s", logs)
		}
		conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if e == nil {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, _ = fmt.Fprint(conn, "*1\r\n$4\r\nPING\r\n")
			line, e := bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
			if e != nil || line != "+PONG\r\n" {
				t.Fatalf("Redis did not answer PING: %q %v", line, e)
			}
			if !r.configMatches("redis", inst.Identity) {
				t.Fatal("running Redis config cannot be recognized for reuse")
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("Redis did not listen on the port from its config")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
