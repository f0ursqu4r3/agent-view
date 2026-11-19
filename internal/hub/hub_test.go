package hub

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-view/internal/collect"
	"agent-view/internal/demo"
	"agent-view/internal/lifecycle"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/wire"
)

// deadline bounds every wait on another goroutine.
const deadline = 5 * time.Second

// tsNow is the time of the fake source's hello and snapshot, like TS in the
// former TypeScript test/hub/helpers.ts (removed in P2c; see git history).
var tsNow = wire.FormatISO(wire.NowMs())

func iso(ms int64) string { return wire.FormatISO(ms) }

// fakeSource is a source the test drives by hand. Like a collector, it sends
// hello and snapshot on Subscribe. It never holds its lock while it calls a
// subscriber or a listener.
type fakeSource struct {
	label      string
	sourceHost string
	// helloNow is the now this source puts in its hello: its own clock. Set
	// it before AddSource.
	helloNow string

	mu        sync.Mutex
	state     string
	seq       int64
	subs      []*fakeSub
	listeners []*fakeListener
}

type fakeSub struct{ fn func(wire.Envelope) }

type fakeListener struct{ fn func() }

func newFake(label string) *fakeSource { return newFakeAs(label, label) }

// newFakeAs returns a source whose envelopes carry sourceHost, not label.
func newFakeAs(label, sourceHost string) *fakeSource {
	return &fakeSource{label: label, sourceHost: sourceHost, helloNow: tsNow, state: "up"}
}

func (f *fakeSource) Label() string { return f.label }
func (f *fakeSource) Kind() string  { return "remote" }

func (f *fakeSource) Subscribe(fn func(wire.Envelope)) func() {
	f.mu.Lock()
	seq := f.seq
	f.mu.Unlock()
	fn(wire.Envelope{V: 1, Host: f.sourceHost, Seq: seq, Ts: f.helloNow,
		Ev: wire.Hello{CollectorVersion: "0", Platform: "test", Now: f.helloNow}})
	fn(wire.Envelope{V: 1, Host: f.sourceHost, Seq: seq, Ts: tsNow, Ev: wire.Snapshot{}})
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
	return wire.HostStatus{Label: f.label, Kind: "remote", State: f.state}
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

func (f *fakeSource) Start() error { return nil }
func (f *fakeSource) Stop()        {}

// emit sends e as the next live envelope.
func (f *fakeSource) emit(e wire.SourceEvent) {
	f.mu.Lock()
	f.seq++
	env := wire.Envelope{V: 1, Host: f.sourceHost, Seq: f.seq, Ts: e.Ts, Ev: e.Ev}
	subs := slices.Clone(f.subs)
	f.mu.Unlock()
	for _, s := range subs {
		s.fn(env)
	}
}

func agent(id string) wire.Agent {
	cwd, root := "/p", "/p"
	return wire.Agent{ID: id, Provider: "claude", Name: id, Role: "main", Link: "root",
		Cwd: &cwd, ProjectRoot: &root, Status: "working", LastEventAt: iso(wire.NowMs())}
}

func upsert(id string) wire.SourceEvent {
	return wire.SourceEvent{Ts: iso(wire.NowMs()), Ev: wire.AgentUpsert{Agent: agent(id)}}
}

// recorder records envelopes. The hub may call fn on a source's goroutine.
type recorder struct {
	mu   sync.Mutex
	envs []wire.Envelope
}

func (r *recorder) fn(e wire.Envelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envs = append(r.envs, e)
}

func (r *recorder) all() []wire.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.envs)
}

// until waits until ok holds for the recorded envelopes.
func (r *recorder) until(t *testing.T, ok func([]wire.Envelope) bool, what string) {
	t.Helper()
	end := time.Now().Add(deadline)
	for !ok(r.all()) {
		if time.Now().After(end) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// hostRecorder records the host lists that OnHostsChange sends.
type hostRecorder struct {
	mu   sync.Mutex
	seen [][]wire.HostStatus
}

func (r *hostRecorder) fn(hosts []wire.HostStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, hosts)
}

func (r *hostRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = nil
}

func (r *hostRecorder) last(t *testing.T) []wire.HostStatus {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		t.Fatal("no host list was sent")
	}
	return r.seen[len(r.seen)-1]
}

func hostKinds(envs []wire.Envelope) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Host + ":" + e.Ev.Kind()
	}
	return out
}

func kinds(envs []wire.Envelope) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Ev.Kind()
	}
	return out
}

func snapshots(envs []wire.Envelope) []wire.Envelope {
	var out []wire.Envelope
	for _, e := range envs {
		if _, ok := e.Ev.(wire.Snapshot); ok {
			out = append(out, e)
		}
	}
	return out
}

func agentIDs(e wire.Envelope) []string {
	var out []string
	for _, a := range e.Ev.(wire.Snapshot).Agents {
		out = append(out, a.ID)
	}
	return out
}

func states(hosts []wire.HostStatus) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.State
	}
	return out
}

func mustAdd(t *testing.T, h *Hub, s Source) {
	t.Helper()
	if err := h.AddSource(s); err != nil {
		t.Fatal(err)
	}
}

// newDemoCollector returns a demo collector on a fake clock that starts at
// the real time, so the demo agents are present for the snapshot.
func newDemoCollector() (*collect.Collector, *demo.FakeScheduler) {
	sched := demo.NewFakeScheduler(float64(wire.NowMs()))
	return collect.New(collect.Options{Host: "local", Demo: true, Sched: sched}), sched
}

// uniqueLabel appends #2, #3 for repeated labels
func TestUniqueLabel(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	for _, c := range []struct{ wanted, want string }{
		{"mac", "mac"}, {"mac", "mac#2"}, {"mac", "mac#3"}, {"other", "other"},
	} {
		if got := h.UniqueLabel(c.wanted); got != c.want {
			t.Fatalf("UniqueLabel(%q) = %q, want %q", c.wanted, got, c.want)
		}
	}
}

// new subscriber receives hello + snapshot per host before live events
func TestSubscribeReplaysHostsBeforeLive(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a, b := newFake("alpha"), newFake("beta")
	mustAdd(t, h, a)
	mustAdd(t, h, b)
	a.emit(upsert("a1"))
	b.emit(upsert("b1"))

	rec := &recorder{}
	h.Subscribe(rec.fn)
	got := rec.all()
	want := []string{"alpha:hello", "alpha:snapshot", "beta:hello", "beta:snapshot"}
	if hk := hostKinds(got); !slices.Equal(hk, want) {
		t.Fatalf("replay = %v, want %v", hk, want)
	}
	snaps := snapshots(got)
	if ids := agentIDs(snaps[0]); !slices.Equal(ids, []string{"a1"}) {
		t.Fatalf("alpha agents = %v", ids)
	}
	if ids := agentIDs(snaps[1]); !slices.Equal(ids, []string{"b1"}) {
		t.Fatalf("beta agents = %v", ids)
	}
	if snaps[0].Seq != 1 {
		t.Fatalf("alpha snapshot seq = %d, want 1", snaps[0].Seq)
	}

	a.emit(upsert("a2"))
	got = rec.all()
	last := got[len(got)-1]
	if last.Host != "alpha" || last.Ev.Kind() != "agent.upsert" || last.Seq != 2 {
		t.Fatalf("last = %s:%s seq %d, want alpha:agent.upsert seq 2", last.Host, last.Ev.Kind(), last.Seq)
	}
	if s := snapshots(h.SnapshotAll())[0]; s.Seq != 2 {
		t.Fatalf("SnapshotAll snapshot seq = %d, want 2", s.Seq)
	}
}

// top-level host is rewritten to the source label
func TestHostRewrittenToSourceLabel(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a := newFakeAs("label", "collector-own-name")
	mustAdd(t, h, a)
	rec := &recorder{}
	h.Subscribe(rec.fn)
	a.emit(upsert("x"))
	for _, envs := range [][]wire.Envelope{rec.all(), h.SnapshotAll()} {
		for _, e := range envs {
			if e.Host != "label" {
				t.Fatalf("%s host = %q, want label", e.Ev.Kind(), e.Host)
			}
		}
	}
}

// snapshotAll includes the latest coverage per host and ignores source.status
func TestSnapshotAllCoverage(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a := newFake("alpha")
	mustAdd(t, h, a)
	cov := func(n int) wire.SourceEvent {
		return wire.SourceEvent{Ts: tsNow, Ev: wire.CoverageReport{Coverage: wire.Coverage{
			LogRoots: []string{}, LogsOpen: n, LogsDiscovered: n, WatchRoots: []string{},
			Sources: map[string]wire.SourceState{},
		}}}
	}
	a.emit(cov(1))
	a.emit(cov(2))
	a.emit(wire.SourceEvent{Ts: tsNow, Ev: wire.SourceStatus{Source: "claude-logs", SourceState: wire.SourceState{State: "degraded"}}})
	all := h.SnapshotAll()
	if k := kinds(all); !slices.Equal(k, []string{"hello", "snapshot", "coverage"}) {
		t.Fatalf("kinds = %v", k)
	}
	if n := all[2].Ev.(wire.CoverageReport).Coverage.LogsOpen; n != 2 {
		t.Fatalf("coverage logsOpen = %d, want 2", n)
	}
}

// a snapshot envelope replaces the mirror; bad input does not break the hub
func TestSnapshotReplacesMirror(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a := newFake("alpha")
	mustAdd(t, h, a)
	a.emit(upsert("old"))
	a.emit(wire.SourceEvent{Ts: tsNow, Ev: wire.Snapshot{Agents: []wire.Agent{agent("fresh")}}})
	rec := &recorder{}
	h.Subscribe(rec.fn)
	if ids := agentIDs(snapshots(rec.all())[0]); !slices.Equal(ids, []string{"fresh"}) {
		t.Fatalf("agents = %v, want [fresh]", ids)
	}

	before := len(rec.all())
	// A source decodes each line and skips the ones that fail, as the remote
	// does. TypeScript forwards these lines: its state throws on the
	// tool.start and the agent.upsert, and its hub stores and replays the
	// null coverage. The Go decoder rejects all three (for coverage a known
	// difference: the last valid coverage stays), so they reach neither the
	// subscribers nor the mirror.
	for _, line := range []string{
		`{"kind":"tool.start","call":null,"ts":"` + tsNow + `"}`,
		`{"kind":"agent.upsert","ts":"` + tsNow + `"}`,
		`{"kind":"coverage","coverage":null,"ts":"` + tsNow + `"}`,
	} {
		e, err := wire.DecodeSourceEvent([]byte(line))
		if err == nil {
			t.Errorf("decode %s: no error", line)
			a.emit(e)
		}
	}
	if n := len(rec.all()); n != before {
		t.Errorf("got %d envelopes, want %d", n, before)
	}
	replay := &recorder{}
	h.Subscribe(replay.fn)
	if got := hostKinds(replay.all()); !slices.Equal(got, []string{"alpha:hello", "alpha:snapshot"}) {
		t.Errorf("replay = %v, want [alpha:hello alpha:snapshot]", got)
	}
	if ids := agentIDs(snapshots(replay.all())[0]); !slices.Equal(ids, []string{"fresh"}) {
		t.Errorf("replayed agents = %v, want [fresh]", ids)
	}
	if calls := snapshots(replay.all())[0].Ev.(wire.Snapshot).OpenCalls; len(calls) != 0 {
		t.Errorf("replayed calls = %+v, want none", calls)
	}
	a.emit(upsert("after"))
	got := rec.all()
	if k := got[len(got)-1].Ev.Kind(); k != "agent.upsert" {
		t.Fatalf("last kind = %s, want agent.upsert", k)
	}
}

// a throwing subscriber does not break other subscribers
func TestPanickingSubscriber(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a := newFake("alpha")
	mustAdd(t, h, a)
	h.Subscribe(func(wire.Envelope) { panic("boom") })
	rec := &recorder{}
	h.Subscribe(rec.fn)
	a.emit(upsert("x"))
	got := rec.all()
	if k := got[len(got)-1].Ev.Kind(); k != "agent.upsert" {
		t.Fatalf("last kind = %s, want agent.upsert", k)
	}
}

// unsubscribe stops delivery
func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a := newFake("alpha")
	mustAdd(t, h, a)
	rec := &recorder{}
	off := h.Subscribe(rec.fn)
	off()
	n := len(rec.all())
	a.emit(upsert("x"))
	if got := len(rec.all()); got != n {
		t.Fatalf("got %d envelopes after unsubscribe, want %d", got, n)
	}
}

// hosts() reports each source status; onHostsChange fires with the new list
func TestHostsAndOnHostsChange(t *testing.T) {
	h := New(Options{})
	t.Cleanup(h.Stop)
	c, _ := newDemoCollector()
	t.Cleanup(c.Stop)
	local := NewLocalSource(c, "local")
	seen := &hostRecorder{}
	off := h.OnHostsChange(seen.fn)
	defer off()
	mustAdd(t, h, local)
	mustAdd(t, h, newFake("remote1"))

	var lk []string
	for _, s := range h.Hosts() {
		lk = append(lk, s.Label+":"+s.Kind)
	}
	if !slices.Equal(lk, []string{"local:local", "remote1:remote"}) {
		t.Fatalf("hosts = %v", lk)
	}
	if s := h.Hosts()[0].State; s != "connecting" {
		t.Fatalf("local state = %s, want connecting", s)
	}
	seen.reset()
	if err := local.Start(); err != nil {
		t.Fatal(err)
	}
	if s := h.Hosts()[0].State; s != "up" {
		t.Fatalf("local state = %s, want up", s)
	}
	if s := states(seen.last(t)); !slices.Equal(s, []string{"up", "up"}) {
		t.Fatalf("last host states = %v, want [up up]", s)
	}
	h.Stop()
	if s := h.Hosts()[0].State; s != "down" {
		t.Fatalf("local state = %s, want down", s)
	}
	if s := seen.last(t)[0].State; s != "down" {
		t.Fatalf("last local state = %s, want down", s)
	}
}

// LocalSource forwards collector envelopes through the hub
func TestLocalSourceForwards(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	c, _ := newDemoCollector()
	t.Cleanup(c.Stop)
	local := NewLocalSource(c, "local")
	mustAdd(t, h, local)
	if err := local.Start(); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	h.Subscribe(rec.fn)
	got := rec.all()
	if len(got) < 2 || !slices.Equal(kinds(got[:2]), []string{"hello", "snapshot"}) {
		t.Fatalf("first kinds = %v, want [hello snapshot ...]", kinds(got))
	}
	hello, ok := got[0].Ev.(wire.Hello)
	if got[0].Host != "local" || !ok || !hello.Demo {
		t.Fatalf("hello = %s %+v, want host local, demo true", got[0].Host, got[0].Ev)
	}
}

// C1: a late subscriber gets a hello with the host clock at replay time, so the viewer skew offset stays near 0
func TestLateSubscriberHelloClock(t *testing.T) {
	t0, _ := lifecycle.ParseISO("2026-01-01T00:00:00.000Z")
	var clock atomic.Int64
	clock.Store(t0)
	h := New(Options{})
	h.now = clock.Load
	defer h.Stop()
	a := newFake("alpha")
	a.helloNow = iso(t0)
	mustAdd(t, h, a)
	// One hour of hub uptime later, a viewer opens.
	clock.Store(t0 + 3_600_000)
	last := iso(t0 + 3_600_000 - 1000)
	ag := agent("a1")
	ag.LastEventAt = last
	a.emit(wire.SourceEvent{Ts: last, Ev: wire.AgentUpsert{Agent: ag}})

	rec := &recorder{}
	st := store.New(store.Options{Now: clock.Load})
	h.Subscribe(func(e wire.Envelope) {
		rec.fn(e)
		env, ok := schema.DecodeLine(wire.EncodeLine(e))
		if !ok {
			t.Errorf("the store cannot decode %s", wire.EncodeLine(e))
			return
		}
		st.Apply(env)
	})
	var hello wire.Hello
	for _, e := range rec.all() {
		if v, ok := e.Ev.(wire.Hello); ok {
			hello = v
			break
		}
	}
	helloMs, ok := lifecycle.ParseISO(hello.Now)
	if !ok || abs(helloMs-(t0+3_600_000)) >= 50 {
		t.Fatalf("hello now = %q, want about %s", hello.Now, iso(t0+3_600_000))
	}
	agents := st.Agents(clock.Load())
	if len(agents) != 1 || agents[0].ID != "a1" {
		t.Fatalf("store agents = %+v, want [a1]", agents)
	}
	if d := abs(agents[0].LastEventMs - lifecycle.MsOf(last)); d >= 50 {
		t.Fatalf("a1 last event is %d ms off", d)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// I1: a host whose clock is 5 minutes behind keeps an agent idle 6 minutes by its own clock in snapshotAll()
func TestHostClockBehind(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	a := newFake("alpha")
	hostNow := wire.NowMs() - 300_000
	a.helloNow = iso(hostNow)
	mustAdd(t, h, a)
	last := iso(hostNow - 360_000)
	ag := agent("r")
	ag.LastEventAt = last
	a.emit(wire.SourceEvent{Ts: last, Ev: wire.AgentUpsert{Agent: ag}})
	if ids := agentIDs(snapshots(h.SnapshotAll())[0]); !slices.Equal(ids, []string{"r"}) {
		t.Fatalf("agents = %v, want [r]", ids)
	}
}

// I4: the hub ticks each mirror, so old files leave it; stop() leaves no timer
func TestTickAgesMirrors(t *testing.T) {
	// The hub clock starts 200 s early, so the old file is inside the file
	// TTL until the test moves the clock: no tick can drop it before the
	// first count.
	base := wire.NowMs()
	var clock atomic.Int64
	clock.Store(base - 200_000)
	h := New(Options{TickMs: 20})
	h.now = clock.Load
	a := newFake("alpha")
	a.helloNow = iso(base - 200_000)
	mustAdd(t, h, a)
	a.emit(wire.SourceEvent{Ts: iso(base - 1_900_000), Ev: wire.FsChange{Path: "/p/old.ts", Op: "change"}})
	a.emit(wire.SourceEvent{Ts: iso(base), Ev: wire.FsChange{Path: "/p/new.ts", Op: "change"}})
	if n := h.StoredFileCount(); n != 2 {
		t.Fatalf("stored files = %d, want 2", n)
	}
	clock.Store(base)
	end := time.Now().Add(deadline)
	for h.StoredFileCount() != 1 {
		if time.Now().After(end) {
			t.Fatalf("stored files = %d, want 1", h.StoredFileCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.tickDone == nil || isClosed(h.tickDone) {
		t.Fatal("the tick does not run")
	}
	h.Stop()
	if !isClosed(h.tickDone) {
		t.Fatal("the tick still runs after Stop")
	}
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// AddSource rejects a label that another source has.
func TestAddSourceDuplicateLabel(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	mustAdd(t, h, newFake("alpha"))
	err := h.AddSource(newFake("alpha"))
	if err == nil || err.Error() != "duplicate host label: alpha" {
		t.Fatalf("err = %v, want duplicate host label: alpha", err)
	}
	if n := len(h.Hosts()); n != 1 {
		t.Fatalf("hosts = %d, want 1", n)
	}
}

// Stop returns while a running demo collector delivers through LocalSource,
// and a subscriber that joins mid-stream gets every envelope once (lock
// order R1).
func TestStopWhileCollectorDelivers(t *testing.T) {
	h := New(Options{TickMs: 5})
	c, sched := newDemoCollector()
	local := NewLocalSource(c, "local")
	// The collector runs first, so Subscribe in AddSource greets on its loop
	// goroutine while the loop delivers.
	if err := local.Start(); err != nil {
		t.Fatal(err)
	}
	quit := make(chan struct{})
	var bg sync.WaitGroup
	defer func() {
		close(quit)
		bg.Wait()
		c.Stop()
	}()
	bg.Go(func() {
		tk := time.NewTicker(time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tk.C:
				sched.Advance(20)
			}
		}
	})

	added := make(chan error, 1)
	go func() { added <- h.AddSource(local) }()
	select {
	case err := <-added:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(deadline):
		t.Fatal("AddSource did not return")
	}

	// A host listener may call the hub: it runs without the hub lock.
	h.OnHostsChange(func([]wire.HostStatus) {
		h.Hosts()
		h.SnapshotAll()
	})
	first := &recorder{}
	h.Subscribe(first.fn)
	first.until(t, func(envs []wire.Envelope) bool { return len(envs) > 30 }, "live envelopes")
	mid := &recorder{}
	h.Subscribe(mid.fn)
	mid.until(t, func(envs []wire.Envelope) bool { return len(envs) > 30 }, "live envelopes after a late subscribe")

	stopped := make(chan struct{})
	var hammer sync.WaitGroup
	hammer.Go(func() {
		for !isClosed(stopped) {
			off := h.Subscribe(func(wire.Envelope) {})
			off()
			time.Sleep(100 * time.Microsecond)
		}
	})
	hammer.Go(func() {
		for !isClosed(stopped) {
			h.SnapshotAll()
			h.Hosts()
			h.StoredFileCount()
			time.Sleep(100 * time.Microsecond)
		}
	})
	go func() {
		h.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(deadline):
		t.Fatal("Stop did not return: deadlock")
	}
	hammer.Wait()

	if s := local.Status().State; s != "down" {
		t.Fatalf("local state = %s, want down", s)
	}
	n := len(first.all())
	time.Sleep(50 * time.Millisecond)
	if got := len(first.all()); got != n {
		t.Fatalf("%d envelopes arrived after Stop", got-n)
	}

	// The late subscriber's replay is hello, snapshot and maybe an older
	// coverage; every live envelope after it has the next seq.
	envs := mid.all()
	if !slices.Equal(kinds(envs[:2]), []string{"hello", "snapshot"}) {
		t.Fatalf("replay kinds = %v", kinds(envs[:2]))
	}
	seq := envs[1].Seq
	live := envs[2:]
	if _, ok := live[0].Ev.(wire.CoverageReport); ok && live[0].Seq <= seq {
		live = live[1:]
	}
	for i, e := range live {
		if e.Seq != seq+int64(i)+1 {
			var trail []string
			for _, x := range live[max(0, i-3):min(len(live), i+3)] {
				trail = append(trail, x.Ev.Kind())
			}
			t.Fatalf("live envelope %d has seq %d, want %d (around: %s)", i, e.Seq, seq+int64(i)+1, strings.Join(trail, " "))
		}
	}
}
