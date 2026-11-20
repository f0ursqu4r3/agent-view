package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"agent-view/internal/lifecycle"
	"agent-view/internal/schema"
)

// Helpers mirror the ones in the former TypeScript test/viewer/store.test.ts
// (removed in P2c; see git history).

var nowMs = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UnixMilli()

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

func ptr[T any](v T) *T { return &v }

func fixed(ms int64) func() int64 { return func() int64 { return ms } }

var seq int64

// env completes e with the envelope fields. ts defaults to nowMs.
func env(host string, e schema.Envelope, ts ...int64) schema.Envelope {
	t := nowMs
	if len(ts) > 0 {
		t = ts[0]
	}
	seq++
	e.V, e.Host, e.Seq, e.Ts = 1, host, seq, iso(t)
	return e
}

type agentOpt func(*schema.Agent)

func withStatus(s string) agentOpt { return func(a *schema.Agent) { a.Status = s } }
func withLastAt(ms int64) agentOpt { return func(a *schema.Agent) { a.LastEventAt = iso(ms) } }
func withParent(id string) agentOpt {
	return func(a *schema.Agent) { a.ParentID, a.Role = ptr(id), "sub-agent" }
}

func agent(id string, opts ...agentOpt) schema.Agent {
	a := schema.Agent{
		ID: id, Provider: "claude", Name: id, Role: "main", Link: "root", Cwd: ptr("/p"),
		ProjectRoot: ptr("/p"), Status: "idle", LastEventAt: iso(nowMs),
	}
	for _, o := range opts {
		o(&a)
	}
	return a
}

type callOpt func(*schema.Call)

func withStart(ms int64) callOpt      { return func(c *schema.Call) { c.StartedAt = iso(ms) } }
func withFiles(f ...string) callOpt   { return func(c *schema.Call) { c.Files = f } }
func withCategory(cat string) callOpt { return func(c *schema.Call) { c.Category = cat } }
func withCommand(cmd string) callOpt {
	return func(c *schema.Call) { c.Category, c.RawName, c.Command = "command", "Bash", cmd }
}
func call(agentID, callID string, opts ...callOpt) schema.Call {
	c := schema.Call{AgentID: agentID, CallID: callID, StartedAt: iso(nowMs), Category: "read", RawName: "Read"}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func evHello(host string, now int64, demo bool) schema.Envelope {
	return env(host, schema.Envelope{Kind: "hello", Now: iso(now), CollectorVersion: "x", Platform: "linux", Demo: demo}, now)
}

func evSnapshot(host string, agents []schema.Agent, calls []schema.Call, files ...schema.FileTouch) schema.Envelope {
	if agents == nil {
		agents = []schema.Agent{}
	}
	if calls == nil {
		calls = []schema.Call{}
	}
	if files == nil {
		files = []schema.FileTouch{}
	}
	raw, err := json.Marshal(files)
	if err != nil {
		panic(err)
	}
	return env(host, schema.Envelope{Kind: "snapshot", Agents: agents, OpenCalls: calls, RawFiles: raw})
}

func evUpsert(host string, a schema.Agent, ts ...int64) schema.Envelope {
	return env(host, schema.Envelope{Kind: "agent.upsert", Agent: &a}, ts...)
}

func evStatus(host, agentID, status string, ts ...int64) schema.Envelope {
	return env(host, schema.Envelope{Kind: "agent.status", AgentID: agentID, Status: status}, ts...)
}

func evStart(host string, c schema.Call, ts ...int64) schema.Envelope {
	return env(host, schema.Envelope{Kind: "tool.start", Call: &c}, ts...)
}

func evUpdate(host, agentID, callID string, files []string, ts ...int64) schema.Envelope {
	raw, err := json.Marshal(files)
	if err != nil {
		panic(err)
	}
	return env(host, schema.Envelope{Kind: "tool.update", AgentID: agentID, CallID: callID, RawFiles: raw}, ts...)
}

func evOutput(host, agentID, callID, chunk string, truncated bool, ts ...int64) schema.Envelope {
	return env(host, schema.Envelope{Kind: "tool.output", AgentID: agentID, CallID: callID, Chunk: chunk, Truncated: truncated}, ts...)
}

func evEnd(host, agentID, callID string, ok *bool, exit *int, writes []schema.Write, ts ...int64) schema.Envelope {
	return env(host, schema.Envelope{Kind: "tool.end", AgentID: agentID, CallID: callID, Ok: ok, ExitCode: exit, Writes: writes}, ts...)
}

func evFS(host, path string, ts ...int64) schema.Envelope {
	return env(host, schema.Envelope{Kind: "fs.change", Path: path, Op: "change"}, ts...)
}

func evCoverage(host string, n int) schema.Envelope {
	c := schema.Coverage{LogRoots: []string{}, LogsOpen: n, LogsDiscovered: n, WatchRoots: []string{}, Sources: map[string]schema.SourceState{}}
	return env(host, schema.Envelope{Kind: "coverage", Coverage: &c})
}

// newStore returns a store that fails the test if Apply recovered a panic. Apply
// turns a panic into "no changes", which many assertions expect, so a panic
// would otherwise pass unseen.
func newStore(t *testing.T, o Options) *Store {
	t.Helper()
	s := New(o)
	t.Cleanup(func() {
		if s.Panics != 0 {
			t.Errorf("Apply recovered %d panic(s)", s.Panics)
		}
	})
	return s
}

func seeded(t *testing.T) *Store {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil))
	return s
}

// Readers of stored state, for assertions the ported API does not cover.

func storedAgentCount(s *Store) int {
	n := 0
	for _, h := range s.hosts {
		n += h.agents.Len()
	}
	return n
}

// storedCallIDs returns the ids of every stored call, sorted.
func storedCallIDs(s *Store) []string {
	ids := []string{}
	for _, h := range s.hosts {
		for _, c := range h.calls.all() {
			ids = append(ids, c.CallID)
		}
	}
	slices.Sort(ids)
	return ids
}

func storedCall(s *Store, agentID, callID string) *ViewCall {
	for _, h := range s.hosts {
		if c, ok := h.calls.get(callKey{agentID, callID}); ok {
			return c
		}
	}
	return nil
}

// openCall is the newest open call of an agent, like ViewerStore.openCall.
func openCall(s *Store, agentID string) *ViewCall {
	if open := s.OpenCalls(agentID); len(open) > 0 {
		return open[0]
	}
	return nil
}

func ids(agents []ViewAgent) []string {
	out := []string{}
	for _, a := range agents {
		out = append(out, a.ID)
	}
	return out
}

func sortedIDs(agents []ViewAgent) []string {
	out := ids(agents)
	slices.Sort(out)
	return out
}

func find(t *testing.T, agents []ViewAgent, id string) ViewAgent {
	t.Helper()
	for _, a := range agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("agent %q not in %q", id, ids(agents))
	return ViewAgent{}
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// TS: "snapshot replaces that host state; deltas update it; other hosts untouched"
func TestSnapshotReplacesHostState(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("h1", nowMs, false))
	s.Apply(evHello("h2", nowMs, false))
	s.Apply(evSnapshot("h1", []schema.Agent{agent("a"), agent("b")}, []schema.Call{call("a", "c1")}))
	s.Apply(evSnapshot("h2", []schema.Agent{agent("z")}, []schema.Call{call("z", "c9")}))
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"a", "b", "z"})
	eq(t, "calls", storedCallIDs(s), []string{"c1", "c9"})

	s.Apply(evUpsert("h1", agent("c")))
	s.Apply(evStatus("h1", "a", "working"))
	if a := find(t, s.Agents(nowMs), "a"); a.Status != "working" || a.Host != "h1" {
		t.Fatalf("a = status %q host %q, want working h1", a.Status, a.Host)
	}
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"a", "b", "c", "z"})

	s.Apply(evSnapshot("h1", []schema.Agent{agent("n")}, nil))
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"n", "z"})
	eq(t, "calls", storedCallIDs(s), []string{"c9"})
	if c := openCall(s, "z"); c == nil || c.CallID != "c9" {
		t.Fatalf("openCall(z) = %+v, want c9", c)
	}
	if c := openCall(s, "a"); c != nil {
		t.Fatalf("openCall(a) = %+v, want nil", c)
	}
}

// TS: "tool lifecycle: start, update, output, end"
func TestToolLifecycle(t *testing.T) {
	s := seeded(t)
	s.Apply(evStart("local", call("a", "c1", withStart(nowMs-3000), withCategory("edit"), withFiles("/p/a.ts")), nowMs-3000))
	c := openCall(s, "a")
	if c == nil || c.Ended || c.StartMs != nowMs-3000 {
		t.Fatalf("open call = %+v, want open, start %d", c, nowMs-3000)
	}
	s.Apply(evUpdate("local", "a", "c1", []string{"/p/b.ts"}, nowMs-2000))
	eq(t, "files", openCall(s, "a").Files, []string{"/p/a.ts", "/p/b.ts"})
	s.Apply(evEnd("local", "a", "c1", ptr(true), nil, nil, nowMs-1000))
	c = storedCall(s, "a", "c1")
	if !c.Ended || c.EndMs != nowMs-1000 || c.Ok == nil || !*c.Ok {
		t.Fatalf("ended call = %+v, want ended at %d ok true", c, nowMs-1000)
	}
	if c := openCall(s, "a"); c != nil {
		t.Fatalf("openCall(a) = %+v, want nil", c)
	}
	if a := find(t, s.Agents(nowMs), "a"); a.Status != "working" {
		t.Fatalf("status = %q, want working", a.Status)
	}
}

// TS: "agents() excludes agents past linger and marks stale working agents"
func TestAgentsExcludesPastLingerAndMarksStale(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{
		agent("fresh", withStatus("working"), withLastAt(nowMs-1000)),
		agent("stale", withStatus("working"), withLastAt(nowMs-130_000)),
		agent("gone", withStatus("idle"), withLastAt(nowMs-600_000)),
		agent("done", withStatus("finished"), withLastAt(nowMs-61_000)),
		agent("parent", withStatus("idle"), withLastAt(nowMs-900_000)),
		agent("kid", withParent("parent"), withStatus("idle"), withLastAt(nowMs-1000)),
	}, nil))
	list := s.Agents(nowMs)
	eq(t, "agents", sortedIDs(list), []string{"fresh", "kid", "parent", "stale"})
	if find(t, list, "fresh").Stale {
		t.Fatal("fresh is stale")
	}
	if !find(t, list, "stale").Stale {
		t.Fatal("stale is not stale")
	}
	if got := find(t, list, "fresh").LastEventMs; got != nowMs-1000 {
		t.Fatalf("fresh LastEventMs = %d, want %d", got, nowMs-1000)
	}
	// lingering agents return on their next event
	s.Apply(evStatus("local", "gone", "working"))
	if !slices.Contains(ids(s.Agents(nowMs)), "gone") {
		t.Fatal("gone did not return on its next event")
	}
	// time passes
	if got := s.Agents(nowMs + 700_000); len(got) != 0 {
		t.Fatalf("agents after 700 s = %q, want none", ids(got))
	}
}

// TS: "Review Focus 2: a host whose hello.now is 5 minutes behind still shows its new events as present
// and in the timeline window" (presence part; the timeline window, calls(window), is not ported).
func TestSkewedHostEventsArePresent(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("remote", nowMs-300_000, false))
	s.Apply(evSnapshot("remote", []schema.Agent{agent("r", withLastAt(nowMs-300_000))}, nil))
	s.Apply(evStart("remote", call("r", "c1", withStart(nowMs-300_000+1000)), nowMs-300_000+1000))
	if c := storedCall(s, "r", "c1"); c == nil || c.StartMs != nowMs-299_000+300_000 {
		t.Fatalf("call = %+v, want start %d", c, nowMs-299_000+300_000)
	}
	list := s.Agents(nowMs)
	eq(t, "agents", ids(list), []string{"r"})
	if list[0].LastEventMs != nowMs-299_000+300_000 {
		t.Fatalf("LastEventMs = %d, want %d", list[0].LastEventMs, nowMs-299_000+300_000)
	}
	if list[0].Stale {
		t.Fatal("r is stale")
	}
}

// TS: "laneOrder nests sub-agents under parents per host"
func TestLaneOrderNestsSubAgents(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("h1", nowMs, false))
	s.Apply(evHello("h2", nowMs, false))
	s.Apply(evSnapshot("h1", []schema.Agent{
		agent("m1"),
		agent("s2", withParent("m1")),
		agent("m2"),
		agent("s1", withParent("m1")),
		agent("g1", withParent("s2")),
		agent("orphan", withParent("missing")),
	}, nil))
	s.Apply(evSnapshot("h2", []schema.Agent{agent("x1")}, nil))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"m1", "s2", "g1", "s1", "m2", "orphan", "x1"})
	// agents seen later go after earlier ones
	s.Apply(evUpsert("h1", agent("m0")))
	s.Apply(evUpsert("h1", agent("s3", withParent("m2"))))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"m1", "s2", "g1", "s1", "m2", "s3", "orphan", "m0", "x1"})
}

// Go only: an agent that is its own parent is a main agent; a parent cycle stays visible.
func TestLaneOrderKeepsParentCycles(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{
		agent("self", withParent("self")),
		agent("m2"),
		agent("c1", withParent("c2")),
		agent("c2", withParent("c1")),
	}, nil))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"self", "m2", "c1", "c2"})
}

// TS: "hosts, coverage, demo flag and focus" (focus is not ported; Hosts is first appearance, not setHosts).
func TestHostsCoverageDemo(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	if s.IsDemo() {
		t.Fatal("empty store is demo")
	}
	eq(t, "hosts", s.Hosts(), nil)
	s.Apply(evHello("local", nowMs, true))
	if !s.IsDemo() {
		t.Fatal("demo hello not demo")
	}
	eq(t, "hosts", s.Hosts(), []string{"local"})
	if _, ok := s.Coverage("local"); ok {
		t.Fatal("coverage before any coverage event")
	}
	s.Apply(evCoverage("local", 1))
	s.Apply(evCoverage("local", 2))
	if c, ok := s.Coverage("local"); !ok || c.LogsOpen != 2 {
		t.Fatalf("coverage = %+v %v, want logsOpen 2", c, ok)
	}
	s.Apply(evHello("h2", nowMs, false))
	s.Apply(evHello("local", nowMs, false))
	eq(t, "hosts", s.Hosts(), []string{"local", "h2"})
	if s.IsDemo() {
		t.Fatal("still demo after a plain hello")
	}
}

// TS: "keeps at most 2000 calls per host, dropping the oldest ended calls first"
func TestKeepsAtMost2000CallsPerHost(t *testing.T) {
	s := seeded(t)
	s.Apply(evStart("local", call("a", "keep-open", withStart(nowMs-1000)), nowMs-1000))
	for i := range 2100 {
		id := fmt.Sprintf("c%d", i)
		s.Apply(evStart("local", call("a", id)))
		s.Apply(evEnd("local", "a", id, ptr(true), nil, nil))
	}
	all := storedCallIDs(s)
	if len(all) != 2000 {
		t.Fatalf("stored calls = %d, want 2000", len(all))
	}
	for id, want := range map[string]bool{"keep-open": true, "c0": false, "c100": false, "c101": true, "c2099": true} {
		if slices.Contains(all, id) != want {
			t.Errorf("call %s stored = %v, want %v", id, !want, want)
		}
	}
}

// TS: "ignores unknown and malformed envelopes without throwing". JSON-level garbage (null, 42, "x",
// a non-string host or chunk) never decodes to an Envelope; its Go equivalents are zero fields.
func TestIgnoresUnknownAndMalformedEnvelopes(t *testing.T) {
	s := seeded(t)
	ts := iso(nowMs)
	v2 := agent("v2")
	bad := []schema.Envelope{
		{},
		{V: 1, Host: "local", Seq: 1, Ts: ts},
		{V: 1, Kind: "hello"},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "nope"},
		{V: 2, Host: "local", Seq: 1, Ts: ts, Kind: "agent.upsert", Agent: &v2},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "snapshot"},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "tool.start"},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "tool.output", AgentID: "a", CallID: "none"},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "fs.change"},
		// Go only: missing objects arrive as nil pointers.
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "agent.upsert"},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "agent.upsert", Agent: &schema.Agent{}},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "coverage"},
		{V: 1, Host: "local", Seq: 1, Ts: ts, Kind: "tool.end", AgentID: "a", CallID: "none"},
	}
	for _, b := range bad {
		if ch := s.Apply(b); ch != nil {
			t.Fatalf("Apply(%+v) = %v, want nil", b, ch)
		}
	}
	eq(t, "agents", ids(s.Agents(nowMs)), []string{"a"})
	eq(t, "hosts", s.Hosts(), []string{"local"})
	if _, ok := s.Coverage("local"); ok {
		t.Fatal("nil coverage stored")
	}
	// In place of takeTouches(NOW + 5000) == []: nothing was held or recorded.
	if h := s.hosts["local"]; len(h.held) != 0 || h.recent.Len() != 0 {
		t.Fatalf("held %v recent %d, want none", h.held, h.recent.Len())
	}
}

// TS: "I1: agents idle for over 2x linger are dropped with their calls; a present child keeps its idle parent"
func TestIdleAgentsDropped(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{
		agent("fresh", withLastAt(nowMs-1000)),
		agent("mid", withLastAt(nowMs-900_000)),
		agent("dead", withLastAt(nowMs-1_300_000)),
		agent("parent", withLastAt(nowMs-1_300_000)),
		agent("kid", withParent("parent"), withLastAt(nowMs-1000)),
	}, []schema.Call{call("dead", "open-dead", withStart(nowMs-1_300_000))}))
	s.Apply(evStart("local", call("dead", "x", withStart(nowMs-1_300_000)), nowMs-1_300_000))
	s.Apply(evEnd("local", "dead", "x", ptr(true), nil, nil, nowMs-1_299_000))
	if n := storedAgentCount(s); n != 5 {
		t.Fatalf("stored = %d, want 5", n)
	}
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"fresh", "parent", "kid"})
	// 'dead' is gone; the old parent stays for its present child; 'mid' (1.5x linger) stays stored but absent
	if n := storedAgentCount(s); n != 4 {
		t.Fatalf("stored = %d, want 4", n)
	}
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"fresh", "kid", "parent"})
	if c := openCall(s, "dead"); c != nil {
		t.Fatalf("openCall(dead) = %+v, want nil", c)
	}
	eq(t, "calls", storedCallIDs(s), []string{})
	// later, the stale ones go too
	eq(t, "lanes", s.LaneOrder(nowMs+2_000), []string{"fresh", "parent", "kid"})
	if n := storedAgentCount(s); n != 4 {
		t.Fatalf("stored = %d, want 4", n)
	}
	s.Apply(evUpsert("local", agent("dead", withLastAt(nowMs))))
	if !slices.Contains(s.LaneOrder(nowMs+3_000), "dead") {
		t.Fatal("dead not back after upsert")
	}
	// sweep runs at most once per second of now
	s.Apply(evSnapshot("local", []schema.Agent{agent("old", withLastAt(nowMs-1_300_000))}, nil))
	if n := storedAgentCount(s); n != 1 {
		t.Fatalf("stored = %d, want 1", n)
	}
	s.Agents(nowMs + 3_500)
	if n := storedAgentCount(s); n != 1 {
		t.Fatalf("stored after 0.5 s = %d, want 1", n)
	}
	s.Agents(nowMs + 4_100)
	if n := storedAgentCount(s); n != 0 {
		t.Fatalf("stored after 1.1 s = %d, want 0", n)
	}
}

// TS: "I1: repeated reads with the same now do not recompute presence, and writes invalidate it"
func TestPresenceCachedPerVersionAndNow(t *testing.T) {
	s := seeded(t)
	s.Agents(nowMs)
	before := s.presenceComputes
	s.Agents(nowMs)
	s.LaneOrder(nowMs)
	s.Agents(nowMs)
	if s.presenceComputes != before {
		t.Fatalf("computes = %d, want %d", s.presenceComputes, before)
	}
	s.Apply(evUpsert("local", agent("b")))
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"a", "b"})
	if s.presenceComputes != before+1 {
		t.Fatalf("computes = %d, want %d", s.presenceComputes, before+1)
	}
	s.Agents(nowMs + 1)
	if s.presenceComputes != before+2 {
		t.Fatalf("computes = %d, want %d", s.presenceComputes, before+2)
	}
}

// A zero or negative duration takes the lifecycle default, and a positive one stays.
func TestNewTakesLifecycleDefaultsForZeroAndNegativeDurations(t *testing.T) {
	want := lifecycle.Defaults()
	for _, o := range []Options{{}, {LingerMs: -1, StaleMs: -1, FinishedFadeMs: -1, FileTTLMs: -1}} {
		if got := New(o).lc; got != want {
			t.Errorf("New(%+v).lc = %+v, want %+v", o, got, want)
		}
	}
	got := New(Options{LingerMs: 1, StaleMs: 2, FinishedFadeMs: 3, FileTTLMs: 4}).lc
	if got.LingerMs != 1 || got.StaleMs != 2 || got.FinishedFadeMs != 3 || got.FileTTLMs != 4 {
		t.Errorf("positive durations changed: %+v", got)
	}
	if DefaultLingerMs != want.LingerMs || DefaultStaleMs != want.StaleMs ||
		DefaultFinishedFadeMs != want.FinishedFadeMs || DefaultFileTTLMs != want.FileTTLMs {
		t.Errorf("store defaults differ from the lifecycle defaults")
	}
}

// TS: "tool.end keeps the exit code on the call"
func TestToolEndKeepsExitCode(t *testing.T) {
	s := seeded(t)
	s.Apply(evStart("local", call("a", "c1", withCommand("false"))))
	if c := storedCall(s, "a", "c1"); c.ExitCode != nil {
		t.Fatalf("exit code before end = %d, want nil", *c.ExitCode)
	}
	s.Apply(evEnd("local", "a", "c1", ptr(false), ptr(1), nil, nowMs+500))
	if c := storedCall(s, "a", "c1"); c.ExitCode == nil || *c.ExitCode != 1 || c.Ok == nil || *c.Ok {
		t.Fatalf("ended call = %+v, want exit 1 ok false", c)
	}
}

// From TS "commands(focus) ... output keeps last 16 KiB" (the output part; commands() is not ported).
func TestToolOutputKeepsLast16KiB(t *testing.T) {
	s := seeded(t)
	s.Apply(evStart("local", call("a", "c4", withCommand("cat"))))
	s.Apply(evOutput("local", "a", "c4", strings.Repeat("x", 4096), false))
	if c := storedCall(s, "a", "c4"); len(c.Output) != 4096 || c.OutputTruncated {
		t.Fatalf("output %d truncated %v, want 4096 false", len(c.Output), c.OutputTruncated)
	}
	for _, ch := range []string{"y", "z", "w", "v"} {
		s.Apply(evOutput("local", "a", "c4", strings.Repeat(ch, 4096), false))
	}
	c := storedCall(s, "a", "c4")
	if len(c.Output) != 16*1024 || !strings.HasPrefix(c.Output, "y") || !strings.HasSuffix(c.Output, "v") || !c.OutputTruncated {
		t.Fatalf("output %d bytes %q…%q truncated %v", len(c.Output), c.Output[:1], c.Output[len(c.Output)-1:], c.OutputTruncated)
	}

	s.Apply(evStart("local", call("a", "c3", withCommand("ls"))))
	s.Apply(evOutput("local", "a", "c3", "hi", true))
	if c := storedCall(s, "a", "c3"); !c.OutputTruncated {
		t.Fatal("truncated flag from the collector not kept")
	}
	s.Apply(evEnd("local", "a", "c3", ptr(false), ptr(2), nil))
	if c := storedCall(s, "a", "c3"); c.Ok == nil || *c.Ok {
		t.Fatalf("ok = %v, want false", c.Ok)
	}

	// Go only: the cut never splits a UTF-8 sequence. 6000 × 3 bytes; the last 16384 start inside a rune.
	s.Apply(evStart("local", call("a", "c5", withCommand("euro"))))
	s.Apply(evOutput("local", "a", "c5", strings.Repeat("€", 6000), false))
	if c := storedCall(s, "a", "c5"); c.Output != strings.Repeat("€", 5461) || !c.OutputTruncated {
		t.Fatalf("output %d bytes truncated %v, want 5461 whole runes", len(c.Output), c.OutputTruncated)
	}
}

// Replaces TS "R25": an agent dropped by the sweep is dormant, and an event brings it back.
func TestDormantAgentRevives(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	idle := nowMs - (2*DefaultLingerMs + 1000)
	s.Apply(evSnapshot("local", []schema.Agent{agent("old", withLastAt(idle)), agent("keep")}, nil))
	eq(t, "agents", ids(s.Agents(nowMs)), []string{"keep"})
	if n := storedAgentCount(s); n != 1 {
		t.Fatalf("stored = %d, want 1", n)
	}
	if _, ok := s.Agent("old"); ok {
		t.Fatal("dormant agent returned by Agent")
	}

	s.Apply(evStart("local", call("old", "c1")))
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"keep", "old"})
	// 'old' keeps its rank; a new rank would put it after 'keep'.
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"old", "keep"})
	if a, ok := s.Agent("old"); !ok || a.Status != "working" || a.Host != "local" {
		t.Fatalf("Agent(old) = %+v %v, want working on local", a, ok)
	}
	if c := openCall(s, "old"); c == nil || c.CallID != "c1" {
		t.Fatalf("openCall(old) = %+v, want c1", c)
	}
	if s.UnknownEvents != 0 || s.hosts["local"].dormant.Len() != 0 {
		t.Fatalf("unknown %d dormant %d, want 0 0", s.UnknownEvents, s.hosts["local"].dormant.Len())
	}
}

// Replaces TS "R25": an event for an agent never seen is dropped and counted.
func TestUnknownAgentEventDropped(t *testing.T) {
	s := seeded(t)
	s.Apply(evStart("local", call("zz", "c1", withFiles("/p/z.ts"))))
	if _, ok := s.Agent("zz"); ok {
		t.Fatal("unknown agent stored")
	}
	eq(t, "agents", ids(s.Agents(nowMs)), []string{"a"})
	eq(t, "calls", storedCallIDs(s), []string{})
	if s.UnknownEvents != 1 {
		t.Fatalf("UnknownEvents = %d, want 1", s.UnknownEvents)
	}
	// Every other agent event for it is dropped too; its write is not recorded.
	s.Apply(evStatus("local", "zz", "working"))
	s.Apply(evUpdate("local", "zz", "c1", []string{"/p/z.ts"}))
	s.Apply(evOutput("local", "zz", "c1", "x", false))
	s.Apply(evEnd("local", "zz", "c1", ptr(true), nil, []schema.Write{{Path: "/p/z.ts", Op: "edit"}}))
	if s.UnknownEvents != 5 {
		t.Fatalf("UnknownEvents = %d, want 5", s.UnknownEvents)
	}
	eq(t, "agents", ids(s.Agents(nowMs)), []string{"a"})
	eq(t, "calls", storedCallIDs(s), []string{})
	if h := s.hosts["local"]; h.recent.Len() != 0 {
		t.Fatalf("recent = %d paths, want 0", h.recent.Len())
	}
}

// A dormant agent also comes back through an upsert (which keeps the tracked status) or a snapshot.
// A revived sub-agent brings back its dormant ancestors, as the TS resnapshot did.
func TestDormantAncestorsReviveWithChild(t *testing.T) {
	idle := nowMs - (2*DefaultLingerMs + 1000)
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{
		agent("P", withLastAt(idle)),
		agent("K", withParent("P"), withLastAt(idle)),
		agent("x"),
		// a dormant parent cycle must not hang the walk
		agent("c1", withParent("c2"), withLastAt(idle)),
		agent("c2", withParent("c1"), withLastAt(idle)),
	}, nil))
	eq(t, "agents", ids(s.Agents(nowMs)), []string{"x"})

	s.Apply(evStart("local", call("K", "k1")))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"P", "K", "x"})
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"K", "P", "x"})

	s.Apply(evStatus("local", "c1", "working"))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"P", "K", "x", "c1", "c2"})
	if n := s.hosts["local"].dormant.Len(); n != 0 || s.UnknownEvents != 0 {
		t.Fatalf("dormant %d unknown %d, want 0 0", n, s.UnknownEvents)
	}
}

func TestDormantRecordReturnsOnUpsertOrSnapshot(t *testing.T) {
	idle := nowMs - (2*DefaultLingerMs + 1000)

	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("old", withLastAt(idle)), agent("keep")}, nil))
	s.Agents(nowMs)
	s.Apply(evUpsert("local", agent("old", withStatus("working"), withLastAt(nowMs))))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"old", "keep"})
	if a, _ := s.Agent("old"); a.Status != "idle" {
		t.Fatalf("status = %q, want the tracked idle", a.Status)
	}

	s = newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("old", withLastAt(idle)), agent("keep")}, nil))
	s.Agents(nowMs)
	s.Apply(evUpsert("local", agent("new")))
	s.Apply(evSnapshot("local", []schema.Agent{agent("keep"), agent("new"), agent("old")}, nil))
	eq(t, "lanes", s.LaneOrder(nowMs), []string{"old", "keep", "new"})
	if n := s.hosts["local"].dormant.Len(); n != 0 {
		t.Fatalf("dormant = %d, want 0", n)
	}
}

func TestDormantCapDropsOldest(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	idle := nowMs - (2*DefaultLingerMs + 1000)
	agents := make([]schema.Agent, maxDormant+1)
	for i := range agents {
		agents[i] = agent(fmt.Sprintf("d%d", i), withLastAt(idle))
	}
	s.Apply(evSnapshot("local", agents, nil))
	s.Agents(nowMs)
	if n := s.hosts["local"].dormant.Len(); n != maxDormant {
		t.Fatalf("dormant = %d, want %d", n, maxDormant)
	}
	s.Apply(evStatus("local", "d0", "working"))
	if s.UnknownEvents != 1 {
		t.Fatalf("UnknownEvents = %d, want 1 (d0 was the oldest)", s.UnknownEvents)
	}
	s.Apply(evStatus("local", "d1", "working"))
	if _, ok := s.Agent("d1"); !ok || s.UnknownEvents != 1 {
		t.Fatalf("d1 not revived; UnknownEvents = %d", s.UnknownEvents)
	}
}

// The held unattributed touches and confirmed write times kept for the unattributed-touch rules.
func TestUnattributedTouchBookkeeping(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil,
		schema.FileTouch{Path: "/p/seed.ts", LastTouchAt: iso(nowMs), Attribution: "confirmed", AgentID: "a"},
		schema.FileTouch{Path: "/p/grey.ts", LastTouchAt: iso(nowMs - 5000), Attribution: "unattributed"},
	))
	h := s.hosts["local"]
	if !slices.Equal(h.held, []heldTouch{{"/p/grey.ts", nowMs - 5000}}) || h.recent.Len() != 0 {
		t.Fatalf("after snapshot: held %v recent %d, want grey.ts only and no writes", h.held, h.recent.Len())
	}
	// Reads, call starts, and updates are not writes.
	s.Apply(evStart("local", call("a", "r1", withFiles("/p/read.ts"))))
	s.Apply(evStart("local", call("a", "e1", withCategory("edit"), withFiles("/p/started.ts"))))
	s.Apply(evUpdate("local", "a", "r1", []string{"/p/r2.ts"}))
	if h.recent.Len() != 0 {
		t.Fatalf("recent = %d paths, want 0", h.recent.Len())
	}
	s.Apply(evFS("local", "/p/x.ts", nowMs+100))
	s.Apply(evEnd("local", "a", "w", ptr(true), nil, []schema.Write{{Path: "/p/x.ts", Op: "edit"}}, nowMs+1500))
	if got := h.held[len(h.held)-1]; got != (heldTouch{"/p/x.ts", nowMs + 100}) {
		t.Fatalf("last held = %v", got)
	}
	if times, _ := h.recent.Get("/p/x.ts"); !slices.Equal(times, []int64{nowMs + 1500}) {
		t.Fatalf("recent x.ts = %v", times)
	}

	// Caps: held 10,000 (oldest out), 16 times per path, 4,000 paths (oldest path out).
	for i := range maxHeld + 50 {
		s.Apply(evFS("local", fmt.Sprintf("/p/f%d.ts", i)))
	}
	if len(h.held) != maxHeld || h.held[0].path != "/p/f50.ts" {
		t.Fatalf("held %d first %q, want %d /p/f50.ts", len(h.held), h.held[0].path, maxHeld)
	}
	for i := range 20 {
		s.Apply(evEnd("local", "a", "w", ptr(true), nil, []schema.Write{{Path: "/p/x.ts"}}, nowMs+int64(i)))
	}
	if times, _ := h.recent.Get("/p/x.ts"); len(times) != maxRecentPerPath || times[0] != nowMs+4 {
		t.Fatalf("recent x.ts = %v, want the last %d", times, maxRecentPerPath)
	}
	for i := range maxRecentPaths {
		s.Apply(evEnd("local", "a", "w", ptr(true), nil, []schema.Write{{Path: fmt.Sprintf("/p/w%d.ts", i)}}))
	}
	if _, ok := h.recent.Get("/p/x.ts"); ok || h.recent.Len() != maxRecentPaths {
		t.Fatalf("recent paths %d, x.ts kept %v; want %d without x.ts", h.recent.Len(), ok, maxRecentPaths)
	}

	// A snapshot replaces both.
	s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil))
	if len(h.held) != 0 || h.recent.Len() != 0 {
		t.Fatalf("after snapshot: held %d recent %d", len(h.held), h.recent.Len())
	}
}
