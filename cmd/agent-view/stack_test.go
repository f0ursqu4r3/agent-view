package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"agent-view/internal/cli"
	"agent-view/internal/hub"
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
// stop(300 ms) returns within 1 s, and its own SIGKILL of the child's group
// (Kill) ends the stub processes within 700 ms of the return. The Remote
// sends its own SIGKILL only 2 s after its SIGTERM, so a child gone before
// 1.5 s was ended by stop, not by the Remote.
func TestStackStopBound(t *testing.T) {
	const bound = 300 * time.Millisecond
	pids := newStubPIDs(t)
	// The shell and its sleep both ignore SIGTERM. The stub records both PIDs.
	cmd := []string{"sh", "-c", "trap '' TERM; sleep 600 & " + pids.record("$$ $!") + "wait"}
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
	pids.waitRunning(2, 5*time.Second)
	began := time.Now()
	s.stop(bound)
	returned := time.Now()
	if took := returned.Sub(began); took < bound-50*time.Millisecond || took > time.Second {
		t.Fatalf("stop took %v, want its %v bound (the child ignores SIGTERM)", took, bound)
	}
	pids.waitGone(time.Until(returned.Add(700*time.Millisecond)), "left 700 ms after stop returned: stop did not send SIGKILL")
	if gone := time.Since(began); gone > 1500*time.Millisecond {
		t.Fatalf("stub processes gone %v after stop began: too late to tell stop from the Remote's own SIGKILL", gone)
	}
	if got := errs.all(); len(got) != 0 {
		t.Fatalf("onErr got %q, want nothing", got)
	}
}

// A panic in the goroutine that start runs goes to onErr; it does not end
// the process. The stack has no local source, so its Start panics.
func TestStackStartRecoversPanic(t *testing.T) {
	s := &stack{hub: hub.New(hub.Options{})}
	defer s.hub.Stop()
	var errs errLog
	s.start(errs.add)
	for deadline := time.Now().Add(5 * time.Second); len(errs.all()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no onErr call after the panic")
		}
	}
	if got := errs.all(); len(got) != 1 || !strings.HasPrefix(got[0], "local collector panicked: ") {
		t.Fatalf("onErr got %q, want one panic report", got)
	}
}

// A panic in the goroutine that stop runs goes to the onErr of start, and
// stop still returns. The stack has no hub, so its Stop panics.
func TestStackStopRecoversPanic(t *testing.T) {
	s := &stack{}
	var errs errLog
	s.onErr = errs.add
	done := make(chan struct{})
	go func() {
		s.stop(time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return")
	}
	if got := errs.all(); len(got) != 1 || !strings.HasPrefix(got[0], "hub stop panicked: ") {
		t.Fatalf("onErr got %q, want one panic report", got)
	}
}
