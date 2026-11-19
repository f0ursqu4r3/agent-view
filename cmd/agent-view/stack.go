package main

import (
	"fmt"
	"time"

	"agent-view/internal/cli"
	"agent-view/internal/collect"
	"agent-view/internal/hub"
	"agent-view/internal/lifecycle"
	"agent-view/internal/remote"
)

// stack is one hub with its local collector and its remote hosts. serve and
// the TUI both run on it.
type stack struct {
	hub     *hub.Hub
	label   string // the local host label
	local   *hub.LocalSource
	remotes []*remote.Remote
}

// newStack builds the hub, the local source and one remote per a.Remotes, in
// that order, and starts none of them. remoteCmd, when not nil, is the argv
// that every remote runs instead of ssh (test only). diag receives the local
// collector's diagnostics; nil keeps its stderr output. On an error the hub is
// stopped and no stack is returned.
func newStack(a cli.Args, remoteCmd []string, diag func(string)) (*stack, error) {
	var command func(sshHost, label string) []string
	if remoteCmd != nil {
		command = func(string, string) []string { return remoteCmd }
	}
	lc := lifecycle.Options{LingerMs: a.LingerMs}
	h := hub.New(hub.Options{Lifecycle: lc})
	s := &stack{hub: h}
	// On an error h.Stop stops the sources added so far; none has started.
	fail := func(err error) (*stack, error) {
		h.Stop()
		return nil, err
	}

	s.label = h.UniqueLabel(localLabel())
	s.local = hub.NewLocalSource(collect.New(collect.Options{
		Host:      s.label,
		Demo:      a.Demo,
		DemoScale: a.DemoScale,
		Watch:     a.Watch,
		Lifecycle: lc,
		Diag:      diag,
	}), s.label)
	if err := h.AddSource(s.local); err != nil {
		return fail(err)
	}
	s.remotes = make([]*remote.Remote, 0, len(a.Remotes))
	for _, sshHost := range a.Remotes {
		r, err := remote.New(remote.Options{SSHHost: sshHost, Label: h.UniqueLabel(sshHost), Command: command})
		if err != nil {
			return fail(fmt.Errorf("remote %s: %w", sshHost, err))
		}
		if err := h.AddSource(r); err != nil {
			return fail(fmt.Errorf("remote %s: %w", sshHost, err))
		}
		s.remotes = append(s.remotes, r)
	}
	return s, nil
}

// start starts the remotes, then the local source. It returns at once: the
// local source starts in its own goroutine, because its Start waits for the
// first scan. A source that fails to start only shows as down; it never stops
// the hub or the other sources. Each failure goes to onErr as "remote <label>
// failed: <message>" or "local collector failed: <message>". onErr may run on
// another goroutine and must return quickly.
func (s *stack) start(onErr func(string)) {
	for _, r := range s.remotes {
		if err := r.Start(); err != nil {
			onErr(fmt.Sprintf("remote %s failed: %v", r.Label(), err))
		}
	}
	go func() {
		if err := s.local.Start(); err != nil {
			onErr(fmt.Sprintf("local collector failed: %v", err))
		}
	}()
}

// stop stops the hub and every source. When the hub has not stopped within
// bound, it sends SIGKILL to the process group of every remote child and
// returns without waiting: a child that ignores SIGTERM gets SIGKILL from its
// Remote only 2 s after SIGTERM, when the process may have exited already.
func (s *stack) stop(bound time.Duration) {
	stopped := make(chan struct{})
	go func() {
		s.hub.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(bound):
		for _, r := range s.remotes {
			r.Kill()
		}
	}
}
