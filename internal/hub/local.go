package hub

import (
	"slices"
	"sync"

	"agent-view/internal/collect"
	"agent-view/internal/wire"
)

// LocalSource is an in-process collector as a hub source. Its status is
// connecting until Start returns, up after (unless a Stop raced it), down
// with the error message when Start fails, and down after Stop.
type LocalSource struct {
	c     *collect.Collector
	label string

	mu        sync.Mutex
	state     string
	detail    string
	listeners []*statusListener
}

type statusListener struct{ fn func() }

// NewLocalSource returns a source for c with the given label. It does not
// start c.
func NewLocalSource(c *collect.Collector, label string) *LocalSource {
	return &LocalSource{c: c, label: label, state: "connecting"}
}

// Label returns the host label.
func (s *LocalSource) Label() string { return s.label }

// Kind returns "local".
func (s *LocalSource) Kind() string { return "local" }

// Subscribe is the collector's Subscribe: fn runs on the collector's loop
// goroutine.
func (s *LocalSource) Subscribe(fn func(wire.Envelope)) (unsubscribe func()) {
	return s.c.Subscribe(fn)
}

// Status returns the host status. Detail is the Start error, if any.
func (s *LocalSource) Status() wire.HostStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wire.HostStatus{Label: s.label, Kind: "local", State: s.state, Detail: s.detail}
}

// OnStatusChange calls fn after every status change, without the source's
// lock. A panic in fn is recovered.
func (s *LocalSource) OnStatusChange(fn func()) (remove func()) {
	l := &statusListener{fn: fn}
	s.mu.Lock()
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.listeners = slices.DeleteFunc(s.listeners, func(x *statusListener) bool { return x == l })
	}
}

// Start starts the collector.
func (s *LocalSource) Start() error {
	if err := s.c.Start(); err != nil {
		s.set("", "down", err.Error())
		return err
	}
	// A Stop that raced with Start wins.
	s.set("connecting", "up", "")
	return nil
}

// Stop stops the collector.
func (s *LocalSource) Stop() {
	defer s.set("", "down", "")
	s.c.Stop()
}

// set changes the status and tells the listeners. When from is not empty,
// it does so only while the status is from.
func (s *LocalSource) set(from, state, detail string) {
	s.mu.Lock()
	if from != "" && s.state != from {
		s.mu.Unlock()
		return
	}
	s.state, s.detail = state, detail
	ls := slices.Clone(s.listeners)
	s.mu.Unlock()
	for _, l := range ls {
		safely(l.fn)
	}
}
