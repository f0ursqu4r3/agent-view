package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-view/internal/cli"
	"agent-view/internal/wire"
)

// errLog collects what start passes to onErr.
type errLog struct {
	mu   sync.Mutex
	msgs []string
}

func (l *errLog) add(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
}

func (l *errLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}

// waitHosts polls the hub's hosts until ok, for up to 5 s.
func waitHosts(t *testing.T, s *stack, ok func([]wire.HostStatus) bool) []wire.HostStatus {
	t.Helper()
	var hosts []wire.HostStatus
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if hosts = s.hub.Hosts(); ok(hosts) {
			return hosts
		}
	}
	t.Fatalf("hosts = %+v", hosts)
	return nil
}

// The stack holds one local source and one remote per --remote, with unique
// labels; start brings the local host up and a failing remote down, and
// reports no error for either.
func TestStackStartHosts(t *testing.T) {
	a := cli.Args{Mode: "serve", Demo: true, Remotes: []string{"box1.invalid", "box1.invalid"}}
	s, err := newStack(a, []string{"sh", "-c", "exit 3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.label != localLabel() || len(s.remotes) != 2 {
		t.Fatalf("label %q, %d remotes", s.label, len(s.remotes))
	}
	var errs errLog
	s.start(errs.add)
	hosts := waitHosts(t, s, func(h []wire.HostStatus) bool {
		return len(h) == 3 && h[0].State == "up" && h[1].State == "down" && h[2].State == "down"
	})
	want := []wire.HostStatus{
		{Label: localLabel(), Kind: "local", State: "up"},
		{Label: "box1.invalid", Kind: "remote", State: "down", Detail: "collector exited 3"},
		{Label: "box1.invalid#2", Kind: "remote", State: "down", Detail: "collector exited 3"},
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Fatalf("hosts = %+v, want %+v", hosts, want)
		}
	}
	s.stop(stopWait)
	if got := errs.all(); len(got) != 0 {
		t.Fatalf("onErr got %q, want nothing", got)
	}
}

// newStack stops the hub and returns the error when a remote host is invalid.
func TestNewStackInvalidRemote(t *testing.T) {
	s, err := newStack(cli.Args{Mode: "serve", Demo: true, Remotes: []string{"a b"}}, nil, nil)
	if s != nil || err == nil || !strings.HasPrefix(err.Error(), "remote a b: ") {
		t.Fatalf("newStack = %v, %v, want nil and a remote error", s, err)
	}
}

// A remote child that ignores SIGTERM does not hold stop beyond its bound:
// stop(1.5 s) returns within 2 s, sends SIGKILL to the child's group, and no
// marked process is left.
func TestStackStopBound(t *testing.T) {
	marker := fmt.Sprintf("sleep 985.%d", time.Now().UnixNano()%1_000_000)
	// The shell and its sleep both ignore SIGTERM, and both command lines hold
	// the marker.
	cmd := []string{"sh", "-c", "trap '' TERM; " + marker + " & wait"}
	s, err := newStack(cli.Args{Mode: "serve", Demo: true, Remotes: []string{"box1.invalid"}}, cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Kill is the stack's own SIGKILL of the remote's group: it ends the child
	// when the test fails before stop has done so.
	t.Cleanup(func() {
		for _, r := range s.remotes {
			r.Kill()
		}
	})
	var errs errLog
	s.start(errs.add)
	for i := 0; pgrepCount(t, marker) != 2; i++ {
		if i == 50 {
			t.Fatalf("%d marked processes, want 2", pgrepCount(t, marker))
		}
		time.Sleep(100 * time.Millisecond)
	}
	began := time.Now()
	s.stop(1500 * time.Millisecond)
	took := time.Since(began)
	if took < 1400*time.Millisecond || took > 2*time.Second {
		t.Fatalf("stop took %v, want its 1.5 s bound (the child ignores SIGTERM)", took)
	}
	for i := 0; ; i++ {
		n := pgrepCount(t, marker)
		if n == 0 {
			break
		}
		if i == 10 {
			t.Fatalf("%d marked processes left 1 s after stop", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := errs.all(); len(got) != 0 {
		t.Fatalf("onErr got %q, want nothing", got)
	}
}
