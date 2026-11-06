//go:build windows

package localproc

import (
	"net"
	"os"
	"strconv"
	"testing"
)

func TestMatchesRejectsStaleIdentity(t *testing.T) {
	id, err := Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !Matches(id) {
		t.Fatal("current identity does not match")
	}
	stale := id
	stale.Created++
	if Matches(stale) {
		t.Fatal("accepted stale creation time")
	}
	stale = id
	stale.Executable += ".other"
	if Matches(stale) {
		t.Fatal("accepted different executable")
	}
}

func TestListenersResolveOwningProcess(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	ids, err := Listeners(port)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id.PID == os.Getpid() && Matches(id) {
			return
		}
	}
	t.Fatal("listener owner missing")
}
