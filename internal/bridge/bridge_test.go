package bridge

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/feed"
	"agent-view/internal/hub"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/wire"
)

// deadline bounds every wait on another goroutine.
const deadline = 5 * time.Second

// fakeSource is a source the test drives by hand. Like a collector, it sends
// hello and snapshot on Subscribe. It never holds its lock while it calls a
// subscriber or a listener.
type fakeSource struct {
	label string

	mu        sync.Mutex
	state     string
	detail    string
	seq       int64
	subs      []*fakeSub
	listeners []*fakeListener
}

type fakeSub struct{ fn func(wire.Envelope) }

type fakeListener struct{ fn func() }

func newFake(label, state string) *fakeSource { return &fakeSource{label: label, state: state} }

func (f *fakeSource) Label() string { return f.label }
func (f *fakeSource) Kind() string  { return "remote" }
func (f *fakeSource) Start() error  { return nil }
func (f *fakeSource) Stop()         {}

func (f *fakeSource) Subscribe(fn func(wire.Envelope)) func() {
	f.mu.Lock()
	seq := f.seq
	f.mu.Unlock()
	now := wire.FormatISO(wire.NowMs())
	fn(wire.Envelope{V: 1, Host: f.label, Seq: seq, Ts: now,
		Ev: wire.Hello{CollectorVersion: "0", Platform: "test", Now: now}})
	fn(wire.Envelope{V: 1, Host: f.label, Seq: seq, Ts: now, Ev: wire.Snapshot{}})
	s := &fakeSub{fn: fn}
	f.mu.Lock()
	f.subs = append(f.subs, s)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.subs = slices.DeleteFunc(f.subs, func(x *fakeSub) bool { return x == s })
	}
}

func (f *fakeSource) Status() wire.HostStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return wire.HostStatus{Label: f.label, Kind: "remote", State: f.state, Detail: f.detail}
}

func (f *fakeSource) OnStatusChange(fn func()) func() {
	l := &fakeListener{fn: fn}
	f.mu.Lock()
	f.listeners = append(f.listeners, l)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.listeners = slices.DeleteFunc(f.listeners, func(x *fakeListener) bool { return x == l })
	}
}

// setState changes the status and tells the listeners.
func (f *fakeSource) setState(state, detail string) {
	f.mu.Lock()
	f.state, f.detail = state, detail
	ls := slices.Clone(f.listeners)
	f.mu.Unlock()
	for _, l := range ls {
		l.fn()
	}
}

// emit sends ev as the next live envelope.
func (f *fakeSource) emit(ev wire.Event) {
	f.mu.Lock()
	f.seq++
	env := wire.Envelope{V: 1, Host: f.label, Seq: f.seq, Ts: wire.FormatISO(wire.NowMs()), Ev: ev}
	subs := slices.Clone(f.subs)
	f.mu.Unlock()
	for _, s := range subs {
		s.fn(env)
	}
}

func agent(id string) wire.Agent {
	cwd, root := "/p", "/p"
	return wire.Agent{ID: id, Provider: "claude", Name: id, Role: "main", Link: "root",
		Cwd: &cwd, ProjectRoot: &root, Status: "working", LastEventAt: wire.FormatISO(wire.NowMs())}
}

func upsert(id string) wire.Event { return wire.AgentUpsert{Agent: agent(id)} }

func toolStart(agentID, callID string) wire.Event {
	return wire.ToolStart{Call: wire.Call{AgentID: agentID, CallID: callID,
		StartedAt: wire.FormatISO(wire.NowMs()), Category: "read", RawName: "Read"}}
}

// newHub returns a started hub with one fake source; the test stops it.
func newHub(t *testing.T, state string) (*hub.Hub, *fakeSource) {
	t.Helper()
	h := hub.New(hub.Options{})
	t.Cleanup(h.Stop)
	src := newFake("box", state)
	if err := h.AddSource(src); err != nil {
		t.Fatal(err)
	}
	return h, src
}

// sent is one message the bridge sent, with the time it arrived.
type sent struct {
	msg tea.Msg
	at  time.Time
}

// sink records the messages and diagnostics of a bridge.
type sink struct {
	mu    sync.Mutex
	msgs  []sent
	diags []string
}

func (s *sink) send(m tea.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, sent{m, time.Now()})
}

func (s *sink) diag(d string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diags = append(s.diags, d)
}

func (s *sink) all() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.msgs)
}

func (s *sink) allDiags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.diags)
}

func (s *sink) envelopes() []schema.Envelope {
	var out []schema.Envelope
	for _, m := range s.all() {
		if b, ok := m.msg.(feed.EnvelopesMsg); ok {
			out = append(out, b...)
		}
	}
	return out
}

func (s *sink) states() []feed.StateMsg {
	var out []feed.StateMsg
	for _, m := range s.all() {
		if st, ok := m.msg.(feed.StateMsg); ok {
			out = append(out, st)
		}
	}
	return out
}

// until waits until ok holds.
func until(t *testing.T, ok func() bool, what string) {
	t.Helper()
	untilWithin(t, deadline, ok, what)
}

func untilWithin(t *testing.T, d time.Duration, ok func() bool, what string) {
	t.Helper()
	end := time.Now().Add(d)
	for !ok() {
		if time.Now().After(end) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func hasDiag(diags []string, part string) bool {
	return slices.ContainsFunc(diags, func(d string) bool { return strings.Contains(d, part) })
}

func TestBridgeOrderAndBatching(t *testing.T) {
	h, src := newHub(t, "up")
	s := &sink{}
	b := Start(Config{Hub: h, Send: s.send, Batch: 20 * time.Millisecond, Diag: s.diag})
	defer b.Stop()
	for i := range 500 {
		src.emit(upsert(fmt.Sprintf("a%03d", i)))
		if i%50 == 0 {
			time.Sleep(3 * time.Millisecond) // spread the envelopes over several intervals
		}
	}
	until(t, func() bool { return len(s.envelopes()) == 502 }, "502 envelopes")

	msgs := s.all()
	if st, ok := msgs[0].msg.(feed.StateMsg); !ok || st != (feed.StateMsg{Host: "box", State: "up"}) {
		t.Fatalf("first message = %#v, want StateMsg{box up}", msgs[0].msg)
	}
	if n := len(s.states()); n != 1 {
		t.Fatalf("got %d StateMsg, want 1", n)
	}
	envs := s.envelopes()
	if envs[0].Kind != "hello" || envs[1].Kind != "snapshot" {
		t.Fatalf("first kinds = %s %s, want hello snapshot", envs[0].Kind, envs[1].Kind)
	}
	for i, e := range envs[2:] {
		if want := fmt.Sprintf("a%03d", i); e.Kind != "agent.upsert" || e.Agent == nil || e.Agent.ID != want {
			t.Fatalf("envelope %d = %s, want the upsert of %s", i+2, e.Kind, want)
		}
	}
	var last time.Time
	batches := 0
	for _, m := range msgs {
		if _, ok := m.msg.(feed.EnvelopesMsg); !ok {
			continue
		}
		batches++
		if !last.IsZero() && m.at.Sub(last) < 19*time.Millisecond {
			t.Fatalf("two batches %v apart, want at least 20 ms", m.at.Sub(last))
		}
		last = m.at
	}
	if batches < 2 {
		t.Fatalf("got %d batches, want several", batches)
	}
}

func TestBridgeHostStates(t *testing.T) {
	h, src := newHub(t, "connecting")
	s := &sink{}
	b := Start(Config{Hub: h, Send: s.send, Batch: 5 * time.Millisecond, Diag: s.diag})
	defer b.Stop()
	until(t, func() bool { return len(s.states()) == 1 }, "connecting")
	src.setState("up", "")
	src.setState("up", "") // no change: no message
	until(t, func() bool { return len(s.states()) == 2 }, "up")
	src.setState("down", "ssh: connection refused")
	until(t, func() bool { return len(s.states()) == 3 }, "down")
	src.setState("down", "ssh: connection refused")
	src.emit(upsert("a1")) // an envelope after the repeat: every earlier entry is done
	until(t, func() bool { return len(s.envelopes()) == 3 }, "the envelope")

	want := []feed.StateMsg{
		{Host: "box", State: "connecting"},
		{Host: "box", State: "up"},
		{Host: "box", State: "down", Detail: "ssh: connection refused"},
	}
	if got := s.states(); !slices.Equal(got, want) {
		t.Fatalf("states = %+v, want %+v", got, want)
	}
}

// gate blocks every Send until it opens.
type gate struct {
	open chan struct{}
	once sync.Once
}

func newGate() *gate { return &gate{open: make(chan struct{})} }

func (g *gate) wait()    { <-g.open }
func (g *gate) release() { g.once.Do(func() { close(g.open) }) }

func TestBridgeNeverBlocksHub(t *testing.T) {
	h, src := newHub(t, "up")
	s := &sink{}
	g := newGate()
	defer g.release()
	b := Start(Config{Hub: h, Send: func(m tea.Msg) { g.wait(); s.send(m) }, Diag: s.diag})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 60_000 {
			src.emit(upsert(fmt.Sprintf("a%d", i%100)))
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatal("the hub took more than 5 s for 60,000 envelopes: the bridge blocks it")
	}

	stopped := make(chan struct{})
	go func() { b.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while Send was blocked")
	case <-time.After(100 * time.Millisecond):
	}
	g.release()
	select {
	case <-stopped:
	case <-time.After(deadline):
		t.Fatal("Stop did not return after the gate opened")
	}
}

func TestBridgeResyncAfterOverflow(t *testing.T) {
	h, src := newHub(t, "up")
	g := newGate()
	defer g.release()
	var mu sync.Mutex
	st := store.New(store.Options{})
	s := &sink{}
	send := func(m tea.Msg) {
		g.wait()
		mu.Lock()
		defer mu.Unlock()
		if batch, ok := m.(feed.EnvelopesMsg); ok {
			for _, e := range batch {
				st.Apply(e)
			}
		}
	}
	b := Start(Config{Hub: h, Send: send, MaxQueue: 1000, Batch: 5 * time.Millisecond, Diag: s.diag})
	defer b.Stop()

	for i := range 1000 {
		id := fmt.Sprintf("a%d", i%300)
		src.emit(upsert(id))
		src.emit(toolStart(id, fmt.Sprintf("c%d", i)))
		if i%3 == 0 {
			ok := true
			src.emit(wire.ToolEnd{AgentID: id, CallID: fmt.Sprintf("c%d", i), Ok: &ok})
		}
	}
	g.release()
	until(t, func() bool { return hasDiag(s.allDiags(), "resynced") }, "the resync diagnostic")
	if !slices.Contains(s.allDiags(), "bridge: the display fell behind; resynced from the hub") {
		t.Fatalf("diagnostics = %q", s.allDiags())
	}

	var snap wire.Snapshot
	for _, e := range h.SnapshotAll() {
		if v, ok := e.Ev.(wire.Snapshot); ok {
			snap = v
		}
	}
	wantAgents := map[string]bool{}
	for _, a := range snap.Agents {
		wantAgents[a.ID] = true
	}
	wantCalls := map[string][]string{}
	for _, c := range snap.OpenCalls {
		wantCalls[c.AgentID] = append(wantCalls[c.AgentID], c.CallID)
	}
	if len(wantAgents) != 300 || len(snap.OpenCalls) == 0 {
		t.Fatalf("hub snapshot has %d agents, %d open calls", len(wantAgents), len(snap.OpenCalls))
	}
	matches := func() (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		agents := st.Agents(wire.NowMs())
		if len(agents) != len(wantAgents) {
			return false, fmt.Sprintf("store has %d agents, hub %d", len(agents), len(wantAgents))
		}
		for _, a := range agents {
			if !wantAgents[a.ID] {
				return false, "store has agent " + a.ID
			}
			var got []string
			for _, c := range st.OpenCalls(a.Key()) {
				got = append(got, c.CallID)
			}
			want := wantCalls[a.ID]
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				return false, fmt.Sprintf("agent %s open calls = %v, hub %v", a.ID, got, want)
			}
		}
		return true, ""
	}
	end := time.Now().Add(deadline)
	for {
		ok, why := matches()
		if ok {
			break
		}
		if time.Now().After(end) {
			t.Fatal(why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBridgeRecoversPanic(t *testing.T) {
	h, src := newHub(t, "up")
	s := &sink{}
	var calls atomic.Int32
	send := func(m tea.Msg) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		s.send(m)
	}
	b := Start(Config{Hub: h, Send: send, Batch: 5 * time.Millisecond, Diag: s.diag})
	defer b.Stop()
	until(t, func() bool { return hasDiag(s.allDiags(), "boom") }, "the panic diagnostic")
	src.emit(upsert("late"))
	until(t, func() bool {
		envs := s.envelopes()
		return len(envs) > 0 && envs[len(envs)-1].Agent != nil && envs[len(envs)-1].Agent.ID == "late"
	}, "a batch after the panic")
}

func TestBridgeStopIsClean(t *testing.T) {
	h, src := newHub(t, "up")
	before := runtime.NumGoroutine()
	var stopped atomic.Bool
	var late atomic.Int32
	s := &sink{}
	send := func(m tea.Msg) {
		if stopped.Load() {
			late.Add(1)
		}
		s.send(m)
	}
	b := Start(Config{Hub: h, Send: send, Batch: 5 * time.Millisecond, Diag: s.diag})

	quit := make(chan struct{})
	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		for i := 0; ; i++ {
			select {
			case <-quit:
				return
			default:
			}
			src.emit(upsert(fmt.Sprintf("a%d", i%10)))
		}
	}()
	until(t, func() bool { return len(s.envelopes()) > 0 }, "a batch")
	b.Stop()
	stopped.Store(true)
	b.Stop()
	time.Sleep(30 * time.Millisecond) // several batch intervals
	close(quit)
	<-emitted
	if n := late.Load(); n != 0 {
		t.Fatalf("%d messages after Stop returned", n)
	}
	untilWithin(t, 2*time.Second, func() bool { return runtime.NumGoroutine() <= before }, "the goroutines to end")
}

// sawAgent reports whether a recorded envelope names the agent, as an upsert
// or in a snapshot.
func (s *sink) sawAgent(id string) bool {
	for _, e := range s.envelopes() {
		if e.Agent != nil && e.Agent.ID == id {
			return true
		}
		if slices.ContainsFunc(e.Agents, func(a schema.Agent) bool { return a.ID == id }) {
			return true
		}
	}
	return false
}

// countDiag counts the diagnostics lines that are exactly line.
func countDiag(diags []string, line string) int {
	n := 0
	for _, d := range diags {
		if d == line {
			n++
		}
	}
	return n
}

// A panic in Send loses its batch. The bridge resyncs from the hub, so the
// replayed hello and snapshot reach the TUI in a later batch.
func TestPanicInSendResyncsFromTheHub(t *testing.T) {
	h, src := newHub(t, "up")
	src.emit(upsert("a1")) // the hub's snapshot holds this agent
	s := &sink{}
	var batches atomic.Int32
	send := func(m tea.Msg) {
		if _, ok := m.(feed.EnvelopesMsg); ok && batches.Add(1) == 1 {
			panic("boom") // the first batch is lost
		}
		s.send(m)
	}
	b := Start(Config{Hub: h, Send: send, Batch: 5 * time.Millisecond, Diag: s.diag})
	defer b.Stop()

	untilWithin(t, 2*time.Second, func() bool { return s.sawAgent("a1") }, "the replayed snapshot")
	envs := s.envelopes()
	if envs[0].Kind != "hello" || envs[1].Kind != "snapshot" {
		t.Fatalf("first kinds after the panic = %s %s, want hello snapshot", envs[0].Kind, envs[1].Kind)
	}
	diags := s.allDiags()
	if !hasDiag(diags, "recovered a panic: boom") {
		t.Fatalf("diagnostics = %q, want the panic", diags)
	}
	if n := countDiag(diags, diagResync); n != 1 {
		t.Fatalf("diagnostics = %q, want one resync line", diags)
	}
}

// A panic that repeats on every batch makes one resync per gap, not a loop,
// and its message is reported once per gap.
func TestRepeatedPanicsResyncOncePerGap(t *testing.T) {
	h, src := newHub(t, "up")
	s := &sink{}
	send := func(m tea.Msg) {
		s.send(m) // record the batch, then fail
		if _, ok := m.(feed.EnvelopesMsg); ok {
			panic("boom")
		}
	}
	b := Start(Config{Hub: h, Send: send, Batch: 5 * time.Millisecond, Diag: s.diag, PanicResyncGap: time.Hour})
	defer b.Stop()

	for i := range 20 {
		id := fmt.Sprintf("a%d", i)
		src.emit(upsert(id))
		until(t, func() bool { return s.sawAgent(id) }, "a batch with "+id)
	}
	diags := s.allDiags()
	if n := countDiag(diags, diagResync); n != 1 {
		t.Fatalf("%d resync lines, want 1; diagnostics = %q", n, diags)
	}
	if n := countDiag(diags, "bridge: recovered a panic: boom"); n != 1 {
		t.Fatalf("%d panic lines, want 1; diagnostics = %q", n, diags)
	}
}

// When the gap is over, the next panic resyncs again.
func TestPanicResyncRepeatsAfterTheGap(t *testing.T) {
	h, src := newHub(t, "up")
	s := &sink{}
	send := func(m tea.Msg) {
		if _, ok := m.(feed.EnvelopesMsg); ok {
			panic("boom")
		}
	}
	b := Start(Config{Hub: h, Send: send, Batch: 5 * time.Millisecond, Diag: s.diag, PanicResyncGap: 20 * time.Millisecond})
	defer b.Stop()

	until(t, func() bool {
		src.emit(upsert("a1")) // each poll gives the bridge a batch to fail on
		return countDiag(s.allDiags(), diagResync) >= 3
	}, "three resyncs")
}

// Start fills in the default gap.
func TestPanicResyncGapDefault(t *testing.T) {
	h, _ := newHub(t, "up")
	b := Start(Config{Hub: h})
	defer b.Stop()
	if b.c.PanicResyncGap != 5*time.Second {
		t.Fatalf("PanicResyncGap = %v, want 5s", b.c.PanicResyncGap)
	}
}

// A panic after the stop began does not ask for a resync, so Stop ends and does
// no hub work after the stop began. The panic is in Send, called by the flush
// of the batch timer; Stop is pending while Send blocks, and the stop path
// itself does not panic.
func TestPanicAfterStopBeganEnds(t *testing.T) {
	h, _ := newHub(t, "up")
	s := &sink{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	var stopped atomic.Bool
	var late atomic.Int32
	send := func(m tea.Msg) {
		if stopped.Load() {
			late.Add(1)
		}
		if _, ok := m.(feed.EnvelopesMsg); !ok {
			return
		}
		enteredOnce.Do(func() { close(entered) })
		<-release
		panic("boom")
	}
	b := Start(Config{Hub: h, Send: send, Batch: 5 * time.Millisecond, Diag: s.diag})
	defer b.Stop()
	defer releaseOnce.Do(func() { close(release) })

	select {
	case <-entered:
	case <-time.After(deadline):
		t.Fatal("Send did not get a batch")
	}
	// Stop begins while Send is in progress; the panic happens after that.
	stopReturned := make(chan struct{})
	go func() { b.Stop(); close(stopReturned) }()
	until(t, func() bool {
		select {
		case <-b.stop:
			return true
		default:
			return false
		}
	}, "the stop to begin")
	releaseOnce.Do(func() { close(release) })
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after a panic in Send")
	}
	stopped.Store(true)

	diags := s.allDiags()
	if !hasDiag(diags, "recovered a panic: boom") {
		t.Fatalf("diagnostics = %q, want the panic", diags)
	}
	if n := countDiag(diags, diagResync); n != 0 {
		t.Fatalf("diagnostics = %q, want no resync after the stop began", diags)
	}
	b.mu.Lock()
	asked := b.resync
	b.mu.Unlock()
	if asked {
		t.Fatal("the panic handler asked for a resync after the stop began")
	}
	if n := late.Load(); n != 0 {
		t.Fatalf("%d messages after Stop returned", n)
	}
}
