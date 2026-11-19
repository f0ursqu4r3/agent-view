package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"agent-view/internal/schema"
)

var kindNames = map[ChangeKind]string{
	CallStarted: "CallStarted", CallEnded: "CallEnded", AgentAdded: "AgentAdded", AgentStatus: "AgentStatus", CallOutput: "CallOutput",
}

func fmtChanges(cs []Change) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "\n\t%s host=%s agent=%s call=%s status=%s at=%d history=%v",
			kindNames[c.Kind], c.Host, c.AgentID, c.CallID, c.Status, c.AtMs, c.History)
	}
	if b.Len() == 0 {
		return " none"
	}
	return b.String()
}

func eqChanges(t *testing.T, what string, got, want []Change) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s:\ngot%s\nwant%s", what, fmtChanges(got), fmtChanges(want))
	}
}

func eqTouches(t *testing.T, what string, got, want []Touch) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %+v, want %+v", what, got, want)
	}
}

func added(host, agentID string, at int64, hist bool) Change {
	return Change{Kind: AgentAdded, Host: host, AgentID: agentID, AtMs: at, History: hist}
}

func started(host, agentID, callID string, at int64, hist bool) Change {
	return Change{Kind: CallStarted, Host: host, AgentID: agentID, CallID: callID, AtMs: at, History: hist}
}

func ended(host, agentID, callID string, at int64, hist bool) Change {
	return Change{Kind: CallEnded, Host: host, AgentID: agentID, CallID: callID, AtMs: at, History: hist}
}

func output(host, agentID, callID string, at int64, hist bool) Change {
	return Change{Kind: CallOutput, Host: host, AgentID: agentID, CallID: callID, AtMs: at, History: hist}
}

func status(host, agentID, st string, at int64, hist bool) Change {
	return Change{Kind: AgentStatus, Host: host, AgentID: agentID, Status: st, AtMs: at, History: hist}
}

func callIDs(calls []*ViewCall) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, c.CallID)
	}
	return out
}

func TestChangesForLifecycle(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	eqChanges(t, "hello", s.Apply(evHello("local", nowMs, false)), nil)
	eqChanges(t, "empty snapshot", s.Apply(evSnapshot("local", nil, nil)), nil)
	eqChanges(t, "upsert", s.Apply(evUpsert("local", agent("a"))), []Change{added("local", "a", nowMs, false)})
	eqChanges(t, "upsert of a known agent", s.Apply(evUpsert("local", agent("a", withStatus("working")))), nil)
	// The call sets the agent working: that is a status change too.
	eqChanges(t, "tool.start", s.Apply(evStart("local", call("a", "c1", withCommand("make")))),
		[]Change{started("local", "a", "c1", nowMs, false), status("local", "a", "working", nowMs, false)})
	eqChanges(t, "tool.output", s.Apply(evOutput("local", "a", "c1", "ok\n", false)), []Change{output("local", "a", "c1", nowMs, false)})
	eqChanges(t, "empty tool.output", s.Apply(evOutput("local", "a", "c1", "", false)), nil)
	eqChanges(t, "truncation flag", s.Apply(evOutput("local", "a", "c1", "", true)), []Change{output("local", "a", "c1", nowMs, false)})
	eqChanges(t, "tool.output for no call", s.Apply(evOutput("local", "a", "none", "x", false)), nil)
	eqChanges(t, "second tool.start while working", s.Apply(evStart("local", call("a", "c2"))), []Change{started("local", "a", "c2", nowMs, false)})
	eqChanges(t, "tool.update", s.Apply(evUpdate("local", "a", "c2", []string{"/p/x.ts"})), nil)
	eqChanges(t, "tool.end ok:false", s.Apply(evEnd("local", "a", "c1", ptr(false), ptr(2), nil, nowMs+500)),
		[]Change{ended("local", "a", "c1", nowMs+500, false)})
	eqChanges(t, "tool.end without a start", s.Apply(evEnd("local", "a", "nostart", ptr(true), nil, nil)), nil)
	eqChanges(t, "agent.status", s.Apply(evStatus("local", "a", "finished", nowMs+600)), []Change{status("local", "a", "finished", nowMs+600, false)})
	eqChanges(t, "same status", s.Apply(evStatus("local", "a", "finished", nowMs+700)), nil)
	eqChanges(t, "sub-agent upsert", s.Apply(evUpsert("local", agent("k", withParent("a")))), []Change{added("local", "k", nowMs, false)})
	eqChanges(t, "event for an unknown agent", s.Apply(evStatus("local", "zz", "working")), nil)
	eqChanges(t, "fs.change", s.Apply(evFS("local", "/p/y.ts")), nil)
	eqChanges(t, "coverage", s.Apply(evCoverage("local", 1)), nil)
}

func TestHistoryFlag(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", nil, nil))
	eqChanges(t, "11 s old", s.Apply(evUpsert("local", agent("a"), nowMs-11_000)), []Change{added("local", "a", nowMs-11_000, true)})
	eqChanges(t, "10 s old", s.Apply(evStart("local", call("a", "c1", withStart(nowMs-10_000)), nowMs-10_000)),
		[]Change{started("local", "a", "c1", nowMs-10_000, false), status("local", "a", "working", nowMs-10_000, false)})
	eqChanges(t, "9 s old", s.Apply(evEnd("local", "a", "c1", ptr(true), nil, nil, nowMs-9000)), []Change{ended("local", "a", "c1", nowMs-9000, false)})

	// The remote clock is 60 s behind: the offset from its hello applies before the rule.
	s.Apply(evHello("remote", nowMs-60_000, false))
	s.Apply(evSnapshot("remote", nil, nil))
	eqChanges(t, "remote 11 s old", s.Apply(evUpsert("remote", agent("r"), nowMs-71_000)), []Change{added("remote", "r", nowMs-11_000, true)})
	eqChanges(t, "remote 9 s old", s.Apply(evStart("remote", call("r", "c1", withStart(nowMs-69_000)), nowMs-69_000)),
		[]Change{started("remote", "r", "c1", nowMs-9000, false), status("remote", "r", "working", nowMs-9000, false)})
}

func TestSnapshotChanges(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	// Each item takes History from its own time.
	got := s.Apply(evSnapshot("local",
		[]schema.Agent{agent("old", withLastAt(nowMs-60_000)), agent("b")},
		[]schema.Call{call("b", "c1", withStart(nowMs-1000)), call("b", "c0", withStart(nowMs-20_000))}))
	eqChanges(t, "first snapshot", got, []Change{
		added("local", "old", nowMs-60_000, true),
		added("local", "b", nowMs, false),
		started("local", "b", "c1", nowMs-1000, false),
		started("local", "b", "c0", nowMs-20_000, true),
	})

	// Re-listed agents and calls still open emit nothing; a new agent and a new open call do.
	agents := []schema.Agent{agent("old"), agent("b"), agent("c")}
	got = s.Apply(evSnapshot("local", agents, []schema.Call{call("b", "c1"), call("b", "c0"), call("c", "c2")}))
	eqChanges(t, "second snapshot", got, []Change{added("local", "c", nowMs, false), started("local", "c", "c2", nowMs, false)})
	if n := s.Counters("b").Calls; n != 2 {
		t.Fatalf("Counters(b).Calls = %d, want 2", n)
	}

	// A call the store saw end is not opened again.
	s.Apply(evEnd("local", "c", "c2", ptr(true), nil, nil))
	got = s.Apply(evSnapshot("local", agents, []schema.Call{call("b", "c1"), call("b", "c0"), call("c", "c2")}))
	eqChanges(t, "snapshot listing an ended call", got, nil)
	eq(t, "open calls of c", callIDs(s.OpenCalls("c")), []string{})

	// An open call a snapshot dropped comes back, silently and not counted again, when a later snapshot lists it.
	eqChanges(t, "snapshot dropping open calls", s.Apply(evSnapshot("local", agents, nil)), nil)
	eq(t, "open calls of b", callIDs(s.OpenCalls("b")), []string{})
	got = s.Apply(evSnapshot("local", agents, []schema.Call{call("b", "c1", withStart(nowMs-1000))}))
	eqChanges(t, "snapshot listing a dropped call", got, nil)
	eq(t, "open calls of b", callIDs(s.OpenCalls("b")), []string{"c1"})
	if n := s.Counters("b").Calls; n != 2 {
		t.Fatalf("Counters(b).Calls = %d, want 2", n)
	}
}

func TestRevivesAreNotAdded(t *testing.T) {
	idle := nowMs - (2*DefaultLingerMs + 1000)
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("P", withLastAt(idle)), agent("K", withParent("P"), withLastAt(idle)), agent("x")}, nil))
	s.Agents(nowMs) // P and K go dormant
	eqChanges(t, "tool.start revives K and P", s.Apply(evStart("local", call("K", "k1"))),
		[]Change{started("local", "K", "k1", nowMs, false), status("local", "K", "working", nowMs, false)})

	s.Agents(nowMs + 2*DefaultLingerMs + 2000) // all three go dormant
	eqChanges(t, "upsert revives x", s.Apply(evUpsert("local", agent("x"))), nil)
	eqChanges(t, "upsert revives K and P", s.Apply(evUpsert("local", agent("K", withParent("P")))), nil)
	// The snapshot leaves K out: K is dormant, not forgotten.
	eqChanges(t, "snapshot re-lists P and x", s.Apply(evSnapshot("local", []schema.Agent{agent("P"), agent("x")}, nil)), nil)
	eq(t, "agents", sortedIDs(s.Agents(nowMs)), []string{"P", "x"})
	eqChanges(t, "status revives K", s.Apply(evStatus("local", "K", "idle")), []Change{status("local", "K", "idle", nowMs, false)})
	if c := s.Counters("K"); c.Calls != 1 || s.UnknownEvents != 0 {
		t.Fatalf("Counters(K) = %+v, UnknownEvents = %d; want 1 call, 0 unknown", c, s.UnknownEvents)
	}
}

func TestReplayIsIgnored(t *testing.T) {
	s := seeded(t)
	start := evStart("local", call("a", "c1", withStart(nowMs-3000)), nowMs-3000)
	if got := s.Apply(start); len(got) == 0 {
		t.Fatal("first tool.start emitted nothing")
	}
	eqChanges(t, "replayed tool.start", s.Apply(start), nil)
	end := evEnd("local", "a", "c1", ptr(true), nil, nil, nowMs-2000)
	eqChanges(t, "tool.end", s.Apply(end), []Change{ended("local", "a", "c1", nowMs-2000, false)})
	eqChanges(t, "replayed tool.end", s.Apply(end), nil)
	s.Apply(evStatus("local", "a", "idle", nowMs-1500))
	before, _ := s.Agent("a")

	// Replays with other times and results change nothing either.
	eqChanges(t, "late tool.start", s.Apply(evStart("local", call("a", "c1", withStart(nowMs+5000)), nowMs+5000)), nil)
	eqChanges(t, "late tool.end", s.Apply(evEnd("local", "a", "c1", ptr(false), ptr(1), []schema.Write{{Path: "/p/x.ts", Op: "edit"}}, nowMs+5000)), nil)
	if after, _ := s.Agent("a"); after.Status != "idle" || after.LastEventMs != before.LastEventMs {
		t.Fatalf("agent = status %q last %d, want idle %d", after.Status, after.LastEventMs, before.LastEventMs)
	}
	eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{})
	recent := s.RecentEnded("a", 5)
	if len(recent) != 1 || recent[0].EndMs != nowMs-2000 || recent[0].Ok == nil || !*recent[0].Ok || recent[0].ExitCode != nil {
		t.Fatalf("RecentEnded = %+v, want c1 ended ok at %d", recent, nowMs-2000)
	}
	if c := s.Counters("a"); c.Calls != 1 || c.Faults != 0 {
		t.Fatalf("Counters = %+v, want 1 call, 0 faults", c)
	}
	if n := s.hosts["local"].recent.len(); n != 0 {
		t.Fatalf("recent writes = %d paths, want 0", n)
	}

	// A replay for a dormant agent does not revive it.
	s.Agents(nowMs + 2*DefaultLingerMs + 6000)
	eqChanges(t, "replay for a dormant agent", s.Apply(start), nil)
	if _, ok := s.Agent("a"); ok || s.UnknownEvents != 0 {
		t.Fatalf("replay revived the agent or counted unknown (%d)", s.UnknownEvents)
	}
}

// A restarted collector sends hello, an empty snapshot, and its scan again.
// The empty snapshot changes no agent state, so the board keeps its agents
// and calls while the scan runs, and the replay changes nothing. (Before,
// the empty snapshot made every agent dormant and dropped its calls.)
func TestRestartedCollectorReplay(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	scan := []schema.Envelope{
		evUpsert("local", agent("a", withLastAt(nowMs-60_000)), nowMs-60_000),
		evStart("local", call("a", "c1", withStart(nowMs-50_000), withCommand("make")), nowMs-50_000),
		evOutput("local", "a", "c1", "built\n", false, nowMs-41_000),
		evEnd("local", "a", "c1", ptr(false), nil, nil, nowMs-40_000),
		evStart("local", call("a", "c2", withStart(nowMs-30_000)), nowMs-30_000),
	}
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", nil, nil))
	for _, e := range scan {
		s.Apply(e)
	}
	want := Counters{Calls: 2, Faults: 1, FirstSeenMs: nowMs - 60_000}
	if c := s.Counters("a"); c != want {
		t.Fatalf("Counters = %+v, want %+v", c, want)
	}
	recent := describe(s.RecentEnded("a", 5))
	before, _ := s.Agent("a")

	s.Apply(evHello("local", nowMs, false))
	eqChanges(t, "empty snapshot", s.Apply(evSnapshot("local", nil, nil)), nil)
	eq(t, "agents after the empty snapshot", ids(s.Agents(nowMs)), []string{"a"})
	eq(t, "open calls after the empty snapshot", callIDs(s.OpenCalls("a")), []string{"c2"})
	eq(t, "ended calls after the empty snapshot", describe(s.RecentEnded("a", 5)), recent)
	for i, e := range scan {
		eqChanges(t, fmt.Sprintf("replay %d (%s)", i, e.Kind), s.Apply(e), nil)
	}
	eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{"c2"})
	// The replayed output of an ended call is not added again.
	eq(t, "ended calls", describe(s.RecentEnded("a", 5)), recent)
	if c := s.Counters("a"); c != want {
		t.Fatalf("Counters = %+v, want %+v", c, want)
	}
	if after, _ := s.Agent("a"); after.Status != before.Status || after.LastEventMs != before.LastEventMs {
		t.Fatalf("agent = %q %d, want %q %d", after.Status, after.LastEventMs, before.Status, before.LastEventMs)
	}
	eqChanges(t, "live end", s.Apply(evEnd("local", "a", "c2", ptr(false), nil, nil, nowMs-1000)), []Change{ended("local", "a", "c2", nowMs-1000, false)})
	if c := s.Counters("a"); c != (Counters{Calls: 2, Faults: 2, FirstSeenMs: nowMs - 60_000}) {
		t.Fatalf("Counters = %+v, want 2 calls, 2 faults", c)
	}
	eq(t, "recent", callIDs(s.RecentEnded("a", 5)), []string{"c2", "c1"})
}

// An empty snapshot changes no agent state, on any host: agents, dormant
// records, calls, pending restores, and counters stay. It still replaces the
// held touches and the write times, as every snapshot does.
func TestEmptySnapshotKeepsAgents(t *testing.T) {
	idle := nowMs - (2*DefaultLingerMs + 1000)
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("a"), agent("d", withLastAt(idle))}, []schema.Call{call("a", "c1")}))
	s.Apply(evEnd("local", "a", "c0", ptr(true), nil, []schema.Write{{Path: "/p/w.ts", Op: "edit"}}))
	s.Apply(evFS("local", "/p/x.ts"))
	s.Agents(nowMs) // d goes dormant
	h := s.hosts["local"]
	if h.dormant.len() != 1 || len(h.held) != 1 || h.recent.len() != 1 {
		t.Fatalf("before: dormant %d held %d recent %d, want 1 1 1", h.dormant.len(), len(h.held), h.recent.len())
	}
	counters := s.Counters("a")

	eqChanges(t, "empty snapshot", s.Apply(evSnapshot("local", nil, nil,
		schema.FileTouch{Path: "/p/seed.ts", LastTouchAt: iso(nowMs), Attribution: "unattributed"})), nil)
	eq(t, "agents", ids(s.Agents(nowMs)), []string{"a"})
	eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{"c1"})
	if c := s.Counters("a"); c != counters {
		t.Fatalf("Counters = %+v, want %+v", c, counters)
	}
	if h.dormant.len() != 1 {
		t.Fatalf("dormant = %d, want 1", h.dormant.len())
	}
	if !slices.Equal(h.held, []heldTouch{{"/p/seed.ts", nowMs}}) || h.recent.len() != 0 {
		t.Fatalf("held %v recent %d, want the snapshot's seed only and no writes", h.held, h.recent.len())
	}
}

// describe lists the fields a restored call must get back.
func describe(calls []*ViewCall) []string {
	out := []string{}
	for _, c := range calls {
		ok, exit := "nil", "nil"
		if c.Ok != nil {
			ok = fmt.Sprint(*c.Ok)
		}
		if c.ExitCode != nil {
			exit = fmt.Sprint(*c.ExitCode)
		}
		out = append(out, fmt.Sprintf("%s %s start=%d end=%d ok=%s exit=%s out=%q files=%q",
			c.CallID, c.Category, c.StartMs, c.EndMs, ok, exit, c.Output, c.Files))
	}
	return out
}

// A replay restores a call that the call list lost, with what its events carry, but emits no
// change and does not count the call again.
func TestReplayRestoresCalls(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	scan := []schema.Envelope{
		evUpsert("local", agent("a", withLastAt(nowMs-60_000)), nowMs-60_000),
		evStart("local", call("a", "c1", withStart(nowMs-50_000), withCommand("make")), nowMs-50_000),
		evOutput("local", "a", "c1", "built\n", false, nowMs-49_000),
		evEnd("local", "a", "c1", ptr(false), ptr(2), nil, nowMs-45_000),
		evStart("local", call("a", "c2", withStart(nowMs-40_000), withCategory("edit"), withFiles("/p/a.ts")), nowMs-40_000),
		evUpdate("local", "a", "c2", []string{"/p/b.ts"}, nowMs-39_000),
		evEnd("local", "a", "c2", ptr(true), nil, nil, nowMs-35_000),
	}
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", nil, nil))
	for _, e := range scan {
		s.Apply(e)
	}
	counters := s.Counters("a")
	before := describe(s.RecentEnded("a", 5))
	if len(before) != 2 {
		t.Fatalf("RecentEnded before the restart = %q, want c2 and c1", before)
	}

	// A snapshot that lists the agent and no call drops the ended calls from the call list.
	relist := evSnapshot("local", []schema.Agent{agent("a", withLastAt(nowMs-35_000))}, nil)
	s.Apply(evHello("local", nowMs, false))
	s.Apply(relist)
	for i, e := range scan {
		eqChanges(t, fmt.Sprintf("replay %d (%s)", i, e.Kind), s.Apply(e), nil)
		if i == 1 {
			// A replayed start of an ended call waits for its end: it is neither open nor ended yet.
			eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{})
			eq(t, "recent", callIDs(s.RecentEnded("a", 5)), []string{})
		}
	}
	eq(t, "restored calls", describe(s.RecentEnded("a", 5)), before)
	if c := s.Counters("a"); c != counters {
		t.Fatalf("Counters = %+v, want %+v", c, counters)
	}

	// A restored call still waiting for its end goes when the sweep makes its agent dormant.
	s.Apply(relist)
	s.Apply(scan[0])
	s.Apply(scan[1])
	s.Agents(nowMs + 2*DefaultLingerMs)
	if n := len(s.hosts["local"].pending); n != 0 {
		t.Fatalf("pending = %d calls, want 0", n)
	}
}

// The review probe: a replayed agent.status older than the agent's status time changes
// nothing, so after a restart the agent stays working on its open call.
func TestReplayedStatusKeepsNewerStatus(t *testing.T) {
	for _, st := range []string{"idle", "interrupted"} {
		t.Run(st, func(t *testing.T) {
			s := newStore(t, Options{Now: fixed(nowMs)})
			scan := []schema.Envelope{
				evUpsert("local", agent("a", withLastAt(nowMs-60_000)), nowMs-60_000),
				evStatus("local", "a", st, nowMs-50_000),
				evStart("local", call("a", "c1", withStart(nowMs-40_000)), nowMs-40_000),
			}
			s.Apply(evHello("local", nowMs, false))
			s.Apply(evSnapshot("local", nil, nil))
			for _, e := range scan {
				s.Apply(e)
			}
			s.Apply(evHello("local", nowMs, false))
			s.Apply(evSnapshot("local", nil, nil))
			for i, e := range scan {
				eqChanges(t, fmt.Sprintf("replay %d (%s)", i, e.Kind), s.Apply(e), nil)
			}
			if a, _ := s.Agent("a"); a.Status != "working" {
				t.Fatalf("status = %q, want working", a.Status)
			}
			eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{"c1"})
		})
	}
}

// The collector ends a call that has had no event for its call timeout with a
// tool.end ok:null at its own clock. The startup scan leaves calls that never
// ended open, so the next tick ends them at now. That end must not make an old
// agent look active: it neither revives nor bumps the agent, and its change
// carries the call's last activity, so it is history.
func TestTimeoutEndOfOldCall(t *testing.T) {
	t.Run("stored agent past the linger", func(t *testing.T) {
		old := nowMs - 15*60_000 // past the linger, not yet dormant
		s := newStore(t, Options{Now: fixed(nowMs)})
		s.Apply(evHello("local", nowMs, false))
		s.Apply(evSnapshot("local", nil, nil))
		s.Apply(evUpsert("local", agent("a", withLastAt(old-1000)), old-1000))
		s.Apply(evStart("local", call("a", "c1", withStart(old-2000)), old-2000))
		s.Apply(evOutput("local", "a", "c1", "", true, old))
		eq(t, "agents before the end", ids(s.Agents(nowMs)), []string{})

		eqChanges(t, "timeout end", s.Apply(evEnd("local", "a", "c1", nil, nil, nil, nowMs)),
			[]Change{ended("local", "a", "c1", old, true)})
		a, ok := s.Agent("a")
		if !ok || a.LastEventMs != old {
			t.Fatalf("Agent(a) = stored %v, LastEventMs %d; want stored, %d", ok, a.LastEventMs, old)
		}
		eq(t, "agents after the end", ids(s.Agents(nowMs)), []string{})
		eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{})
		recent := s.RecentEnded("a", 5)
		if len(recent) != 1 || recent[0].Ok != nil || recent[0].EndMs != old {
			t.Fatalf("RecentEnded = %q, want c1 ended ok:null at %d", describe(recent), old)
		}
		if c := s.Counters("a"); c.Calls != 1 || c.Faults != 0 {
			t.Fatalf("Counters = %+v, want 1 call, 0 faults", c)
		}
	})

	t.Run("dormant agent", func(t *testing.T) {
		old := nowMs - 20*3_600_000
		s := newStore(t, Options{Now: fixed(nowMs)})
		s.Apply(evHello("local", nowMs, false))
		s.Apply(evSnapshot("local", nil, nil))
		s.Apply(evUpsert("local", agent("a", withLastAt(old)), old))
		start := evStart("local", call("a", "c1", withStart(old)), old)
		s.Apply(start)
		eq(t, "agents before the end", ids(s.Agents(nowMs)), []string{}) // the sweep makes a dormant

		eqChanges(t, "timeout end", s.Apply(evEnd("local", "a", "c1", nil, nil, nil, nowMs)), nil)
		if _, ok := s.Agent("a"); ok || s.UnknownEvents != 0 {
			t.Fatalf("the end revived the agent, or counted it unknown (%d)", s.UnknownEvents)
		}
		eq(t, "agents after the end", ids(s.Agents(nowMs)), []string{})

		// The call ended: when the agent comes back, a replayed start does not open it again.
		s.Apply(evStatus("local", "a", "working", nowMs))
		eqChanges(t, "replayed start", s.Apply(start), nil)
		eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{})
		if c := s.Counters("a"); c.Calls != 1 || c.Faults != 0 {
			t.Fatalf("Counters = %+v, want 1 call, 0 faults", c)
		}
	})

	t.Run("live agent", func(t *testing.T) {
		s := newStore(t, Options{Now: fixed(nowMs)})
		s.Apply(evHello("local", nowMs, false))
		s.Apply(evSnapshot("local", nil, nil))
		s.Apply(evUpsert("local", agent("a", withLastAt(nowMs-800_000)), nowMs-800_000))
		s.Apply(evStart("local", call("a", "c1", withStart(nowMs-700_000)), nowMs-700_000))
		s.Apply(evStart("local", call("a", "c2", withStart(nowMs-3000)), nowMs-3000))
		// The agent is active: the end is live, at its last event, and the call shows ??.
		eqChanges(t, "timeout end", s.Apply(evEnd("local", "a", "c1", nil, nil, nil, nowMs)),
			[]Change{ended("local", "a", "c1", nowMs-3000, false)})
		if a, _ := s.Agent("a"); a.LastEventMs != nowMs-3000 {
			t.Fatalf("LastEventMs = %d, want %d", a.LastEventMs, nowMs-3000)
		}
		eq(t, "open calls", callIDs(s.OpenCalls("a")), []string{"c2"})
	})
}

func TestStatusOrderedByEventTime(t *testing.T) {
	s := seeded(t)
	eqChanges(t, "finished", s.Apply(evStatus("local", "a", "finished", nowMs-10_000)),
		[]Change{status("local", "a", "finished", nowMs-10_000, false)})
	eqChanges(t, "older status", s.Apply(evStatus("local", "a", "working", nowMs-20_000)), nil)
	// A call that started before the status time opens, but does not make the agent working.
	eqChanges(t, "older tool.start", s.Apply(evStart("local", call("a", "c1", withStart(nowMs-15_000)), nowMs-15_000)),
		[]Change{started("local", "a", "c1", nowMs-15_000, true)})
	if a, _ := s.Agent("a"); a.Status != "finished" {
		t.Fatalf("status = %q, want finished", a.Status)
	}
	// One log record sends working, its tool events, then idle, all at one time: equal times apply in order.
	at := nowMs - 5000
	eqChanges(t, "working", s.Apply(evStatus("local", "a", "working", at)), []Change{status("local", "a", "working", at, false)})
	eqChanges(t, "tool.start", s.Apply(evStart("local", call("a", "c2", withStart(at)), at)), []Change{started("local", "a", "c2", at, false)})
	eqChanges(t, "idle", s.Apply(evStatus("local", "a", "idle", at)), []Change{status("local", "a", "idle", at, false)})
	// The status time travels through the dormant map.
	s.Agents(nowMs + 2*DefaultLingerMs + 1000)
	eqChanges(t, "older status for a dormant agent", s.Apply(evStatus("local", "a", "working", at-1)), nil)
	if a, ok := s.Agent("a"); !ok || a.Status != "idle" {
		t.Fatalf("Agent(a) = status %q, stored %v; want idle, revived", a.Status, ok)
	}
	// A snapshot that re-lists the agent keeps the status time.
	s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil))
	eqChanges(t, "older status after a snapshot", s.Apply(evStatus("local", "a", "working", at-1)), nil)

	// A dormant parent that wakes with its child keeps its status time.
	s = newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("P"), agent("K", withParent("P"))}, nil))
	s.Apply(evStatus("local", "P", "finished", at))
	s.Agents(nowMs + 2*DefaultLingerMs + 1000)
	s.Apply(evStatus("local", "K", "working", at))
	eqChanges(t, "older status for a woken parent", s.Apply(evStatus("local", "P", "working", at-1)), nil)
}

func TestCountersSurviveTrim(t *testing.T) {
	s := seeded(t)
	for i := range 2100 {
		id := fmt.Sprintf("c%d", i)
		s.Apply(evStart("local", call("a", id)))
		s.Apply(evEnd("local", "a", id, ptr(true), nil, nil))
	}
	if c := s.Counters("a"); c.Calls != 2100 || c.Faults != 0 {
		t.Fatalf("Counters = %+v, want 2100 calls, 0 faults", c)
	}
	recent := s.RecentEnded("a", 5000)
	if len(recent) != 2000 || recent[0].CallID != "c2099" {
		t.Fatalf("RecentEnded = %d calls, first %q; want 2000, c2099", len(recent), recent[0].CallID)
	}
}

func TestCountersTravelThroughDormancy(t *testing.T) {
	s := seeded(t)
	s.Apply(evStart("local", call("a", "c1", withStart(nowMs-5000)), nowMs-5000))
	s.Apply(evEnd("local", "a", "c1", ptr(false), nil, nil, nowMs-4000))
	open := evStart("local", call("a", "c2", withStart(nowMs-3000)), nowMs-3000)
	s.Apply(open)
	want := Counters{Calls: 2, Faults: 1, FirstSeenMs: nowMs - 5000}
	if c := s.Counters("a"); c != want {
		t.Fatalf("Counters = %+v, want %+v", c, want)
	}

	later := nowMs + 2*DefaultLingerMs + 1000
	s.Agents(later)
	if c := s.Counters("a"); c != (Counters{}) {
		t.Fatalf("Counters of a dormant agent = %+v, want zero", c)
	}
	s.Apply(evStatus("local", "a", "working", later))
	if c := s.Counters("a"); c != want {
		t.Fatalf("Counters after the sweep = %+v, want %+v", c, want)
	}
	// The sweep dropped the open call c2: its tool.start restores it silently and does not count it again.
	eq(t, "open calls after the sweep", callIDs(s.OpenCalls("a")), []string{})
	eqChanges(t, "replayed start of a dropped call", s.Apply(open), nil)
	eq(t, "open calls after the replay", callIDs(s.OpenCalls("a")), []string{"c2"})
	if c := s.Counters("a"); c != want {
		t.Fatalf("Counters after the replay = %+v, want %+v", c, want)
	}
	// A snapshot that leaves the agent out keeps its record too.
	s.Apply(evSnapshot("local", []schema.Agent{agent("other")}, nil))
	s.Apply(evUpsert("local", agent("a"), later))
	if c := s.Counters("a"); c != want {
		t.Fatalf("Counters after the snapshot = %+v, want %+v", c, want)
	}
	// Scan events arrive out of order: an older record lowers FirstSeenMs.
	s.Apply(evStatus("local", "a", "idle", nowMs-9000))
	if got := s.Counters("a").FirstSeenMs; got != nowMs-9000 {
		t.Fatalf("FirstSeenMs = %d, want %d", got, nowMs-9000)
	}
}

func TestFaultsCountOnlyFalse(t *testing.T) {
	s := seeded(t)
	for i, ok := range []*bool{ptr(false), nil, ptr(true)} {
		id := fmt.Sprintf("c%d", i)
		s.Apply(evStart("local", call("a", id)))
		s.Apply(evEnd("local", "a", id, ok, nil, nil))
	}
	if c := s.Counters("a"); c.Calls != 3 || c.Faults != 1 {
		t.Fatalf("Counters = %+v, want 3 calls, 1 fault", c)
	}
}

func TestRecentEndedOrdersByEndTime(t *testing.T) {
	s := seeded(t)
	base := nowMs - 60_000
	for i, id := range []string{"c1", "c2", "c3"} {
		at := base + int64(i)*1000
		s.Apply(evStart("local", call("a", id, withStart(at)), at))
	}
	// Ends arrive c2 (end 5 s) then c1 (end 9 s).
	s.Apply(evEnd("local", "a", "c2", ptr(true), nil, nil, base+5000))
	s.Apply(evEnd("local", "a", "c1", ptr(true), nil, nil, base+9000))
	eq(t, "recent", callIDs(s.RecentEnded("a", 10)), []string{"c1", "c2"})
	// c3 ends earliest but arrives last: neither arrival nor insertion order puts it last.
	s.Apply(evEnd("local", "a", "c3", ptr(true), nil, nil, base+3000))
	eq(t, "recent", callIDs(s.RecentEnded("a", 10)), []string{"c1", "c2", "c3"})
	eq(t, "recent 2", callIDs(s.RecentEnded("a", 2)), []string{"c1", "c2"})
	// Equal end times: the last inserted first. Open calls are not listed.
	s.Apply(evStart("local", call("a", "c4", withStart(base+4000)), base+4000))
	s.Apply(evStart("local", call("a", "c5", withStart(base+4500)), base+4500))
	s.Apply(evStart("local", call("a", "open", withStart(base+4600)), base+4600))
	s.Apply(evEnd("local", "a", "c5", ptr(true), nil, nil, base+9000))
	s.Apply(evEnd("local", "a", "c4", ptr(true), nil, nil, base+9000))
	eq(t, "recent ties", callIDs(s.RecentEnded("a", 4)), []string{"c5", "c4", "c1", "c2"})
	eq(t, "recent 0", callIDs(s.RecentEnded("a", 0)), []string{})
	eq(t, "recent of an unknown agent", callIDs(s.RecentEnded("zz", 5)), []string{})
}

func TestOpenCallsNewestFirst(t *testing.T) {
	s := seeded(t)
	for _, c := range []struct {
		id string
		at int64
	}{{"c1", nowMs - 3000}, {"c2", nowMs - 1000}, {"c3", nowMs - 2000}} {
		s.Apply(evStart("local", call("a", c.id, withStart(c.at)), c.at))
	}
	eq(t, "open", callIDs(s.OpenCalls("a")), []string{"c2", "c3", "c1"})
	// Equal start times: the last inserted first. An ended call is not open.
	s.Apply(evStart("local", call("a", "c4", withStart(nowMs-1000)), nowMs-1000))
	s.Apply(evEnd("local", "a", "c3", ptr(true), nil, nil))
	eq(t, "open", callIDs(s.OpenCalls("a")), []string{"c4", "c2", "c1"})
	eq(t, "open calls of an unknown agent", callIDs(s.OpenCalls("zz")), []string{})
}

func TestHostStats(t *testing.T) {
	s := newStore(t, Options{Now: fixed(nowMs)})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("a"), agent("b")}, nil))
	for _, c := range []schema.Call{call("a", "c1"), call("a", "c2"), call("b", "c3")} {
		s.Apply(evStart("local", c))
	}
	s.Apply(evEnd("local", "a", "c1", ptr(true), nil, nil))
	if a, c := s.HostStats("local"); a != 2 || c != 3 {
		t.Fatalf("HostStats = %d agents, %d calls; want 2, 3", a, c)
	}
	// Dormant agents and their calls count: they were loaded too.
	s.Agents(nowMs + 2*DefaultLingerMs + 1000)
	if n := storedAgentCount(s); n != 0 {
		t.Fatalf("stored = %d, want 0", n)
	}
	if a, c := s.HostStats("local"); a != 2 || c != 3 {
		t.Fatalf("HostStats with dormant agents = %d agents, %d calls; want 2, 3", a, c)
	}
	if a, c := s.HostStats("nope"); a != 0 || c != 0 {
		t.Fatalf("HostStats(nope) = %d, %d; want 0, 0", a, c)
	}
}

func TestUnattributedRules(t *testing.T) {
	write := func(path string) []schema.Write { return []schema.Write{{Path: path, Op: "edit"}} }

	// TS: "unattributed touch is held 2 s and suppressed by a confirmed touch of the same path"
	t.Run("held 2 s and suppressed by a write during the hold", func(t *testing.T) {
		s := seeded(t)
		s.Apply(evFS("local", "/p/x.ts", nowMs))
		s.Apply(evFS("local", "/p/y.ts", nowMs))
		eqTouches(t, "at +1999", s.TakeUnattributed(nowMs+1999), nil)
		s.Apply(evEnd("local", "a", "w", ptr(true), nil, write("/p/x.ts"), nowMs+1500))
		eqTouches(t, "at +2000", s.TakeUnattributed(nowMs+2000), []Touch{{Host: "local", Path: "/p/y.ts", AtMs: nowMs}})
		eqTouches(t, "at +5000", s.TakeUnattributed(nowMs+5000), nil)
	})

	// TS: "unattributed touch is also suppressed by a confirmed touch up to 2 s before it"
	t.Run("suppressed by a write up to 2 s before", func(t *testing.T) {
		s := seeded(t)
		s.Apply(evEnd("local", "a", "w", ptr(true), nil, write("/p/x.ts"), nowMs))
		eqTouches(t, "at now", s.TakeUnattributed(nowMs), nil)
		s.Apply(evFS("local", "/p/x.ts", nowMs+2000))
		s.Apply(evFS("local", "/p/x.ts", nowMs+2001))
		eqTouches(t, "at +4001", s.TakeUnattributed(nowMs+4001), []Touch{{Host: "local", Path: "/p/x.ts", AtMs: nowMs + 2001}})
		// Write times older than 4 s can suppress no held touch, so the take prunes them.
		if n := s.hosts["local"].recent.len(); n != 0 {
			t.Fatalf("recent writes = %d paths, want 0", n)
		}
	})

	// TS: "Minor 2: only a confirmed write suppresses a grey flicker; reads, call starts and snapshot seeds do not"
	t.Run("only a confirmed write suppresses", func(t *testing.T) {
		s := newStore(t, Options{Now: fixed(nowMs)})
		s.Apply(evHello("local", nowMs, false))
		s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil,
			schema.FileTouch{Path: "/p/seed.ts", LastTouchAt: iso(nowMs), Attribution: "confirmed", AgentID: "a"}))
		s.Apply(evStart("local", call("a", "r1", withFiles("/p/read.ts")), nowMs))
		s.Apply(evStart("local", call("a", "e1", withCategory("edit"), withFiles("/p/started.ts")), nowMs))
		s.Apply(evEnd("local", "a", "w1", ptr(true), nil, write("/p/written.ts"), nowMs))
		for _, p := range []string{"/p/seed.ts", "/p/read.ts", "/p/started.ts", "/p/written.ts"} {
			s.Apply(evFS("local", p, nowMs+500))
		}
		eqTouches(t, "at now", s.TakeUnattributed(nowMs), nil)
		eqTouches(t, "at +2500", s.TakeUnattributed(nowMs+2500), []Touch{
			{Host: "local", Path: "/p/seed.ts", AtMs: nowMs + 500},
			{Host: "local", Path: "/p/read.ts", AtMs: nowMs + 500},
			{Host: "local", Path: "/p/started.ts", AtMs: nowMs + 500},
		})
	})

	// TS: "I3: touches older than the file lifetime at take time are dropped (history replay is not live)",
	// and the unattributed seed of TS "touch sources", which is held like an fs.change.
	t.Run("older than the file TTL dropped", func(t *testing.T) {
		s := newStore(t, Options{Now: fixed(nowMs)})
		s.Apply(evHello("local", nowMs, false))
		old := nowMs - 2*3_600_000
		s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil,
			schema.FileTouch{Path: "/p/grey.ts", LastTouchAt: iso(nowMs - 5000), Attribution: "unattributed"},
			schema.FileTouch{Path: "/p/old-seed.ts", LastTouchAt: iso(old), Attribution: "unattributed"}))
		s.Apply(evFS("local", "/p/old-grey.ts", old))
		s.Apply(evFS("local", "/p/at-ttl.ts", nowMs-DefaultFileTTLMs))
		s.Apply(evFS("local", "/p/in-ttl.ts", nowMs-DefaultFileTTLMs+1))
		eqTouches(t, "at now", s.TakeUnattributed(nowMs), []Touch{
			{Host: "local", Path: "/p/grey.ts", AtMs: nowMs - 5000},
			{Host: "local", Path: "/p/in-ttl.ts", AtMs: nowMs - DefaultFileTTLMs + 1},
		})
		eqTouches(t, "at +5000", s.TakeUnattributed(nowMs+5000), nil)
	})
}

// A panic while applying one envelope drops that envelope. The clock is the only
// caller-supplied code that Apply runs, so a failing clock forces the panic path.
func TestApplyRecoversFromPanic(t *testing.T) {
	boom := false
	s := New(Options{Now: func() int64 {
		if boom {
			panic("clock failed")
		}
		return nowMs
	}})
	s.Apply(evHello("local", nowMs, false))
	s.Apply(evSnapshot("local", []schema.Agent{agent("a")}, nil))
	boom = true
	got := s.Apply(evStatus("local", "a", "working"))
	boom = false
	if got != nil || s.Panics != 1 {
		t.Fatalf("Apply = %v, Panics = %d; want nil, 1", got, s.Panics)
	}
	if a, _ := s.Agent("a"); a.Status != "idle" {
		t.Fatalf("status = %q, want idle: the envelope is dropped", a.Status)
	}
	eqChanges(t, "next envelope", s.Apply(evStatus("local", "a", "working")), []Change{status("local", "a", "working", nowMs, false)})
}
