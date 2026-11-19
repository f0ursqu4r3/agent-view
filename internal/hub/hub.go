// Package hub merges the sources of agent activity: the local collector and
// remote collectors. It ports the former TypeScript hub/hub.ts (removed in
// P2c; see git history).
package hub

import (
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"agent-view/internal/collect"
	"agent-view/internal/lifecycle"
	"agent-view/internal/safe"
	"agent-view/internal/wire"
)

// defaultTickMs is how often the hub ages each mirror (ends timed-out calls,
// drops old files).
const defaultTickMs = 1000

// Source is one host's stream of envelopes. Subscribe sends hello and
// snapshot first, then every live envelope. OnStatusChange tells the hub when
// Status changes. A source never holds its own lock while it calls a
// subscriber or a status listener.
type Source interface {
	Label() string
	// Kind is "local" or "remote".
	Kind() string
	Subscribe(fn func(wire.Envelope)) (unsubscribe func())
	Status() wire.HostStatus
	Start() error
	Stop()
	OnStatusChange(fn func()) (remove func())
}

// Options configure a Hub.
type Options struct {
	// Lifecycle are the mirrors' durations; a zero field takes its default.
	Lifecycle lifecycle.Options
	// TickMs is how often the hub ages each mirror; 0 means 1,000 ms.
	TickMs int64
}

// mirror is the hub's copy of one host's state.
type mirror struct {
	source Source
	label  string
	state  *collect.State
	hello  *wire.Envelope
	// offset is hub clock minus host clock, measured when the hello arrived.
	offset   int64
	coverage *wire.Envelope
	seq      int64
	// off and offStatus are set once AddSource has subscribed.
	off       func()
	offStatus func()
}

type subscriber struct {
	fn      func(wire.Envelope)
	removed atomic.Bool
}

func (s *subscriber) send(env wire.Envelope) { safe.Call(func() { s.fn(env) }) }

func (s *subscriber) isRemoved() bool { return s.removed.Load() }

type hostListener struct{ fn func([]wire.HostStatus) }

// Hub merges the sources. For each host it keeps a collect.State mirror, so a
// new subscriber gets hello, snapshot, and coverage first, then live
// envelopes. Each mirror is judged by its host's clock: the hub keeps the
// skew it saw at the host's hello.
//
// Lock order: one mutex guards the mirrors, the subscribers and the tick.
// The hub never holds it while it calls into a source or a host listener.
// It holds it while it calls a subscriber, so that Subscribe has no gap.
type Hub struct {
	opts Options
	// now is the hub clock in Unix ms. Tests set it before AddSource.
	now func() int64

	mu       sync.Mutex
	mirrors  []*mirror // in the order the sources were added
	labels   map[string]bool
	subs     []*subscriber
	stopped  bool
	tickStop chan struct{}
	tickDone chan struct{}

	stopDone chan struct{} // closed when the first Stop has finished

	listenMu  sync.Mutex
	listeners []*hostListener
}

// New returns a hub with no sources.
func New(o Options) *Hub {
	if o.TickMs <= 0 {
		o.TickMs = defaultTickMs
	}
	return &Hub{opts: o, now: wire.NowMs, labels: map[string]bool{}, stopDone: make(chan struct{})}
}

// UniqueLabel returns wanted the first time. Later uses return wanted#2,
// wanted#3, and so on.
func (h *Hub) UniqueLabel(wanted string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	label := wanted
	for n := 2; h.labels[label]; n++ {
		label = wanted + "#" + strconv.Itoa(n)
	}
	h.labels[label] = true
	return label
}

// AddSource adds s and subscribes to it. It fails when another source has the
// same label, or when the hub is stopped (TypeScript subscribes even then).
// It does not start s.
func (h *Hub) AddSource(s Source) error {
	label := s.Label()
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return errors.New("hub is stopped")
	}
	if slices.ContainsFunc(h.mirrors, func(m *mirror) bool { return m.label == label }) {
		h.mu.Unlock()
		return errors.New("duplicate host label: " + label)
	}
	h.labels[label] = true
	m := &mirror{source: s, label: label, state: collect.NewState(h.opts.Lifecycle)}
	h.mirrors = append(h.mirrors, m)
	h.startTicking()
	h.mu.Unlock()

	// Outside the lock: a collector delivers hello and snapshot to ingest
	// inside Subscribe.
	offStatus := s.OnStatusChange(h.notifyHosts)
	off := s.Subscribe(func(e wire.Envelope) { h.ingest(m, e) })

	h.mu.Lock()
	late := h.stopped
	if !late {
		m.off, m.offStatus = off, offStatus
	}
	h.mu.Unlock()
	if late {
		// Stop took the mirror before it had these.
		safe.Call(off)
		safe.Call(offStatus)
	}
	h.notifyHosts()
	return nil
}

// Subscribe sends fn each host's hello, snapshot and coverage (SnapshotAll),
// then every later envelope, with no gap and no duplicate. The returned
// function stops delivery; fn may call it.
//
// fn runs while the hub's lock is held, on the goroutine of the source that
// sent the envelope (for the replay, on the caller's). Calls to fn never
// overlap. fn must return quickly, and it must not call the hub's methods
// (Subscribe, Stop, AddSource, SnapshotAll, Hosts, StoredFileCount,
// UniqueLabel), which take the same lock. A panic in fn is recovered.
func (h *Hub) Subscribe(fn func(wire.Envelope)) (unsubscribe func()) {
	s := &subscriber{fn: fn}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, env := range h.snapshotAll() {
		s.send(env)
	}
	if !h.stopped {
		h.subs = slices.DeleteFunc(h.subs, (*subscriber).isRemoved)
		h.subs = append(h.subs, s)
	}
	return func() { s.removed.Store(true) }
}

// SnapshotAll returns each host's hello, snapshot, and coverage, for the
// hosts whose hello has arrived. The hello carries the host's clock now, not
// at its first hello: a viewer measures skew from it. The snapshot is judged
// at the host's now.
func (h *Hub) SnapshotAll() []wire.Envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotAll()
}

func (h *Hub) snapshotAll() []wire.Envelope {
	out := []wire.Envelope{}
	hubNow := h.now()
	for _, m := range h.mirrors {
		if m.hello == nil {
			continue
		}
		now := hubNow - m.offset
		ts := wire.FormatISO(now)
		hello := *m.hello
		ev := hello.Ev.(wire.Hello)
		ev.Now = ts
		hello.Ev, hello.Ts = ev, ts
		out = append(out, hello,
			wire.Envelope{V: wire.SchemaVersion, Host: m.label, Seq: m.seq, Ts: ts, Ev: m.state.Snapshot(now)})
		if m.coverage != nil {
			out = append(out, *m.coverage)
		}
	}
	return out
}

// StoredFileCount returns the file touches held by all mirrors. It is a
// diagnostic.
func (h *Hub) StoredFileCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.mirrors {
		n += m.state.FileCount()
	}
	return n
}

// Hosts returns each source's status, in the order the sources were added.
func (h *Hub) Hosts() []wire.HostStatus {
	h.mu.Lock()
	srcs := make([]Source, len(h.mirrors))
	for i, m := range h.mirrors {
		srcs[i] = m.source
	}
	h.mu.Unlock()
	out := make([]wire.HostStatus, len(srcs))
	for i, s := range srcs {
		out[i] = s.Status()
	}
	return out
}

// OnHostsChange calls fn with the new host list when a source is added, when
// a source's status changes, and once more at the end of Stop. fn runs
// without the hub's lock, on the goroutine that made the change, and calls
// may overlap. fn must not call Stop. A panic in fn is recovered.
func (h *Hub) OnHostsChange(fn func([]wire.HostStatus)) (remove func()) {
	l := &hostListener{fn: fn}
	h.listenMu.Lock()
	h.listeners = append(h.listeners, l)
	h.listenMu.Unlock()
	return func() {
		h.listenMu.Lock()
		defer h.listenMu.Unlock()
		h.listeners = slices.DeleteFunc(h.listeners, func(x *hostListener) bool { return x == l })
	}
}

// Stop ends the tick, unsubscribes every source, stops them all at the same
// time and waits for them, then tells the host listeners once more. Nothing
// is delivered to subscribers after Stop starts. A second call waits for the
// first.
func (h *Hub) Stop() {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		<-h.stopDone
		return
	}
	h.stopped = true
	h.subs = nil
	mirrors := slices.Clone(h.mirrors)
	offs := make([]func(), len(mirrors))
	offStatus := make([]func(), len(mirrors))
	for i, m := range mirrors {
		offs[i], offStatus[i] = m.off, m.offStatus
	}
	tickStop, tickDone := h.tickStop, h.tickDone
	h.mu.Unlock()
	defer close(h.stopDone)

	if tickStop != nil {
		close(tickStop)
		<-tickDone
	}
	for _, off := range offs {
		safe.Call(off)
	}
	var wg sync.WaitGroup
	for _, m := range mirrors {
		wg.Go(func() { safe.Call(m.source.Stop) })
	}
	wg.Wait()
	for _, off := range offStatus {
		safe.Call(off)
	}
	h.notifyHosts()
	h.listenMu.Lock()
	h.listeners = nil
	h.listenMu.Unlock()
}

// ingest records one envelope of m's source and forwards it. It runs on the
// source's goroutine.
func (h *Hub) ingest(m *mirror, env wire.Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	env.Host = m.label
	m.seq = env.Seq
	h.record(m, env)
	h.subs = slices.DeleteFunc(h.subs, (*subscriber).isRemoved)
	for _, s := range h.subs {
		if !s.isRemoved() {
			s.send(env)
		}
	}
}

// record updates m with env. Bad input must not break the hub.
func (h *Hub) record(m *mirror, env wire.Envelope) {
	defer func() { _ = recover() }()
	switch ev := env.Ev.(type) {
	case wire.Hello:
		m.hello = &env
		m.offset = 0
		if t, ok := lifecycle.ParseISO(ev.Now); ok {
			m.offset = h.now() - t
		}
	case wire.Snapshot:
		m.state.Load(ev)
	case wire.CoverageReport:
		m.coverage = &env
	case wire.SourceStatus:
		// A source's health is not part of the mirror.
	default:
		m.state.Apply(wire.SourceEvent{Ts: env.Ts, Ev: env.Ev})
	}
}

// startTicking starts the tick goroutine once. The caller holds h.mu.
func (h *Hub) startTicking() {
	if h.tickStop != nil || h.stopped {
		return
	}
	h.tickStop = make(chan struct{})
	h.tickDone = make(chan struct{})
	go h.tickLoop(time.Duration(h.opts.TickMs)*time.Millisecond, h.tickStop, h.tickDone)
}

func (h *Hub) tickLoop(d time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h.tick()
		}
	}
}

// tick ages every mirror at its host's clock. The events Tick returns are
// dropped: each collector sends its own timed-out tool.end.
func (h *Hub) tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	hubNow := h.now()
	for _, m := range h.mirrors {
		safe.Call(func() { m.state.Tick(hubNow - m.offset) })
	}
}

func (h *Hub) notifyHosts() {
	h.listenMu.Lock()
	ls := slices.Clone(h.listeners)
	h.listenMu.Unlock()
	if len(ls) == 0 {
		return
	}
	hosts := h.Hosts()
	for _, l := range ls {
		safe.Call(func() { l.fn(slices.Clone(hosts)) })
	}
}
