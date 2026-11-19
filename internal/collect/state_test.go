package collect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"agent-view/internal/lifecycle"
	"agent-view/internal/wire"
)

var base = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UnixMilli()

const aID = "mac/claude:default:s1"

func iso(offsetMs int64) string { return wire.FormatISO(base + offsetMs) }

func sp(s string) *string { return &s }

func bp(b bool) *bool { return &b }

func mkAgent(id string, mod func(*wire.Agent)) wire.Agent {
	a := wire.Agent{
		ID: id, Provider: "claude", Name: id, Role: "main", Link: "root",
		Cwd: sp("/p"), ProjectRoot: sp("/p"), Status: "idle", LastEventAt: iso(0),
	}
	if mod != nil {
		mod(&a)
	}
	return a
}

func upsert(a wire.Agent) wire.SourceEvent { return upsertAt(a, a.LastEventAt) }

func upsertAt(a wire.Agent, ts string) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.AgentUpsert{Agent: a}}
}

func mkCall(agentID, callID string, mod func(*wire.Call)) wire.Call {
	c := wire.Call{AgentID: agentID, CallID: callID, StartedAt: iso(0), Category: "read", RawName: "Read"}
	if mod != nil {
		mod(&c)
	}
	return c
}

func start(c wire.Call) wire.SourceEvent { return startAt(c, c.StartedAt) }

func startAt(c wire.Call, ts string) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.ToolStart{Call: c}}
}

func end(agentID, callID, ts string, writes []wire.Write) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.ToolEnd{AgentID: agentID, CallID: callID, Ok: bp(true), Writes: writes}}
}

func status(agentID, st, ts string) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.AgentStatusChange{AgentID: agentID, Status: st}}
}

func fsChange(path, ts string) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.FsChange{Path: path, Op: "change"}}
}

func update(agentID, callID string, files []string, ts string) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.ToolUpdate{AgentID: agentID, CallID: callID, Files: files}}
}

func output(agentID, callID, ts string) wire.SourceEvent {
	return wire.SourceEvent{Ts: ts, Ev: wire.ToolOutput{AgentID: agentID, CallID: callID, Chunk: "x"}}
}

// forwarded applies e and fails the test when the state drops it.
func forwarded(t *testing.T, s *State, e wire.SourceEvent) wire.SourceEvent {
	t.Helper()
	out, ok := s.Apply(e)
	if !ok {
		t.Fatalf("Apply dropped %#v", e)
	}
	return out
}

func dropped(t *testing.T, s *State, e wire.SourceEvent) {
	t.Helper()
	if _, ok := s.Apply(e); ok {
		t.Fatalf("Apply forwarded %#v, want a drop", e)
	}
}

func lastEvent(t *testing.T, s *State, id string) string {
	t.Helper()
	a, ok := s.Agent(id)
	if !ok {
		t.Fatalf("agent %q missing", id)
	}
	return a.LastEventAt
}

func callIDs(calls []wire.Call) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, c.CallID)
	}
	return out
}

func filePaths(files []wire.FileTouch) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func equal(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func newState() *State { return NewState(lifecycle.Options{}) }

// history loading cannot push old events ahead of new events
func TestHistoryLoadingCannotPushOldEventsAheadOfNewEvents(t *testing.T) {
	s := newState()
	forwarded(t, s, upsertAt(mkAgent(aID, func(a *wire.Agent) { a.LastEventAt = iso(5_000) }), iso(5_000)))
	forwarded(t, s, status(aID, "working", iso(1_000)))
	equal(t, lastEvent(t, s, aID), iso(5_000))
	forwarded(t, s, upsertAt(mkAgent(aID, func(a *wire.Agent) { a.LastEventAt = iso(2_000) }), iso(2_000)))
	equal(t, lastEvent(t, s, aID), iso(5_000))
	forwarded(t, s, status(aID, "idle", iso(9_000)))
	equal(t, lastEvent(t, s, aID), iso(9_000))
}

// apply drops duplicate tool.start and tool.end for the same agentId+callId (daemon/log overlap)
func TestApplyDropsDuplicateToolStartAndToolEnd(t *testing.T) {
	s := newState()
	forwarded(t, s, upsert(mkAgent(aID, nil)))
	st := start(mkCall(aID, "c1", nil))
	forwarded(t, s, st)
	dropped(t, s, st)
	en := end(aID, "c1", iso(1_000), nil)
	forwarded(t, s, en)
	dropped(t, s, en)
	// An ended call cannot be started again.
	dropped(t, s, st)
	// Same callId on another agent is a different call.
	forwarded(t, s, start(mkCall("other", "c1", nil)))
}

// unknown tool.end is forwarded once and remembered as ended; output and update are forwarded
func TestUnknownToolEndIsForwardedOnce(t *testing.T) {
	s := newState()
	en := end(aID, "ghost", iso(0), nil)
	equal(t, forwarded(t, s, en), en)
	dropped(t, s, en)
	out := wire.SourceEvent{Ts: iso(0), Ev: wire.ToolOutput{AgentID: aID, CallID: "x", Chunk: "hi"}}
	upd := update(aID, "x", []string{"/p/a"}, iso(0))
	equal(t, forwarded(t, s, out), out)
	equal(t, forwarded(t, s, upd), upd)
}

// tool.start sets the agent working; tool.end writes add confirmed FileTouch; fs.change adds unattributed FileTouch without agentId
func TestToolStartSetsWorkingAndFileTouches(t *testing.T) {
	s := newState()
	forwarded(t, s, upsert(mkAgent(aID, nil)))
	if a, _ := s.Agent(aID); a.Status != "idle" {
		t.Fatalf("status = %q, want idle", a.Status)
	}
	forwarded(t, s, startAt(mkCall(aID, "c1", func(c *wire.Call) {
		c.Category, c.RawName, c.Files = "edit", "Edit", []string{"/p/in.ts"}
	}), iso(1_000)))
	if a, _ := s.Agent(aID); a.Status != "working" {
		t.Fatalf("status = %q, want working", a.Status)
	}
	forwarded(t, s, end(aID, "c1", iso(2_000), []wire.Write{{Path: "/p/out.ts", Op: "create"}}))
	forwarded(t, s, fsChange("/p/disk.ts", iso(3_000)))
	files := s.Snapshot(base + 4_000).Files
	equal(t, files, []wire.FileTouch{
		{Path: "/p/disk.ts", LastTouchAt: iso(3_000), Attribution: "unattributed"},
		{Path: "/p/in.ts", LastTouchAt: iso(1_000), Attribution: "confirmed", AgentID: aID},
		{Path: "/p/out.ts", LastTouchAt: iso(2_000), Attribution: "confirmed", AgentID: aID},
	})
}

// file touches: search never touches; latest touch wins per path; tool.update touches and extends the open call
func TestFileTouchRules(t *testing.T) {
	s := newState()
	forwarded(t, s, upsert(mkAgent(aID, nil)))
	forwarded(t, s, startAt(mkCall(aID, "g", func(c *wire.Call) {
		c.Category, c.RawName, c.Pattern, c.Files = "search", "Grep", "x", []string{"/p/s.ts"}
	}), iso(100)))
	forwarded(t, s, update(aID, "g", []string{"/p/s2.ts"}, iso(200)))
	forwarded(t, s, startAt(mkCall(aID, "b", func(c *wire.Call) {
		c.Category, c.RawName, c.Command = "command", "Bash", "ls"
	}), iso(300)))
	forwarded(t, s, update(aID, "b", []string{"/p/u.ts"}, iso(400)))
	forwarded(t, s, fsChange("/p/u.ts", iso(500)))
	forwarded(t, s, fsChange("/p/u.ts", iso(450))) // older: ignored
	snap := s.Snapshot(base + 1_000)
	equal(t, snap.Files, []wire.FileTouch{{Path: "/p/u.ts", LastTouchAt: iso(500), Attribution: "unattributed"}})
	idx := slices.IndexFunc(snap.OpenCalls, func(c wire.Call) bool { return c.CallID == "b" })
	if idx < 0 {
		t.Fatal("open call b missing")
	}
	equal(t, snap.OpenCalls[idx].Files, []string{"/p/u.ts"})
	forwarded(t, s, end(aID, "b", iso(600), []wire.Write{{Path: "/p/u.ts", Op: "edit"}}))
	snap = s.Snapshot(base + 1_000)
	equal(t, snap.Files, []wire.FileTouch{{Path: "/p/u.ts", LastTouchAt: iso(600), Attribution: "confirmed", AgentID: aID}})
}

// agent.upsert merges descriptive fields and sets status only for a new agent
func TestAgentUpsertMergesDescriptiveFields(t *testing.T) {
	s := newState()
	forwarded(t, s, upsert(mkAgent(aID, func(a *wire.Agent) { a.Status = "working" })))
	forwarded(t, s, status(aID, "idle", iso(1_000)))
	forwarded(t, s, upsertAt(mkAgent(aID, func(a *wire.Agent) {
		a.Status, a.Name, a.Link, a.Model = "working", "renamed", "confirmed", "m1"
	}), iso(2_000)))
	a, _ := s.Agent(aID)
	if a.Name != "renamed" || a.Link != "confirmed" || a.Model != "m1" || a.Status != "idle" {
		t.Fatalf("agent = %#v", a)
	}
	// An incoming agent without a model keeps the known model.
	forwarded(t, s, upsertAt(mkAgent(aID, nil), iso(3_000)))
	a, _ = s.Agent(aID)
	if a.Model != "m1" {
		t.Fatalf("model = %q, want m1", a.Model)
	}
}

// tick ends calls open longer than 10 minutes with ok:null and no writes
func TestTickEndsCallsOpenLongerThanTenMinutes(t *testing.T) {
	s := newState()
	forwarded(t, s, upsert(mkAgent(aID, nil)))
	forwarded(t, s, start(mkCall(aID, "old", nil)))
	forwarded(t, s, start(mkCall(aID, "live", nil)))
	forwarded(t, s, output(aID, "live", iso(300_000)))
	equal(t, len(s.Tick(base+599_999)), 0)
	now := base + 600_000
	out := s.Tick(now)
	equal(t, out, []wire.SourceEvent{{Ts: wire.FormatISO(now), Ev: wire.ToolEnd{AgentID: aID, CallID: "old", Ok: nil}}})
	if te := out[0].Ev.(wire.ToolEnd); te.Writes != nil {
		t.Fatalf("writes = %#v, want absent", te.Writes)
	}
	equal(t, len(s.Tick(now+1)), 0)
	equal(t, callIDs(s.Snapshot(now).OpenCalls), []string{"live"})
	// The timed-out call is remembered as ended.
	dropped(t, s, end(aID, "old", iso(700_000), nil))
	out = s.Tick(base + 300_000 + 600_000)
	equal(t, len(out), 1)
	equal(t, out[0].Ev.(wire.ToolEnd).CallID, "live")
}

// tick drops file touches older than 30 minutes
func TestTickDropsOldFileTouches(t *testing.T) {
	s := newState()
	forwarded(t, s, fsChange("/p/a", iso(0)))
	forwarded(t, s, fsChange("/p/b", iso(1_000)))
	s.Tick(base + 1_800_000)
	equal(t, filePaths(s.Snapshot(base+1_800_000).Files), []string{"/p/b"})
	s.Tick(base + 1_801_000)
	equal(t, len(s.Snapshot(base+1_801_000).Files), 0)
}

// snapshot holds only present agents and their ancestors, their open calls, and files touched within 30 minutes
func TestSnapshotHoldsOnlyPresentState(t *testing.T) {
	s := newState()
	root := mkAgent("root", func(a *wire.Agent) { a.LastEventAt = iso(-3_000_000) })
	child := mkAgent("child", func(a *wire.Agent) {
		a.ParentID, a.Role, a.LastEventAt = sp("root"), "sub-agent", iso(-1_000)
	})
	gone := mkAgent("gone", func(a *wire.Agent) { a.LastEventAt = iso(-3_000_000) })
	for _, a := range []wire.Agent{root, child, gone} {
		forwarded(t, s, upsert(a))
	}
	forwarded(t, s, startAt(mkCall("gone", "g1", func(c *wire.Call) { c.StartedAt = iso(-3_000_000) }), iso(-3_000_000)))
	forwarded(t, s, startAt(mkCall("root", "r1", func(c *wire.Call) { c.StartedAt = iso(-3_000_000) }), iso(-3_000_000)))
	forwarded(t, s, fsChange("/p/fresh", iso(-1_000)))
	forwarded(t, s, fsChange("/p/stale", iso(-1_800_001)))
	snap := s.Snapshot(base)
	ids := []string{}
	for _, a := range snap.Agents {
		ids = append(ids, a.ID)
	}
	equal(t, ids, []string{"child", "root"})
	equal(t, callIDs(snap.OpenCalls), []string{"r1"})
	equal(t, filePaths(snap.Files), []string{"/p/fresh"})
}

// load replaces all state and clears ended-call memory
func TestLoadReplacesAllState(t *testing.T) {
	s := newState()
	forwarded(t, s, upsert(mkAgent("old", nil)))
	forwarded(t, s, start(mkCall("old", "c", nil)))
	forwarded(t, s, end("old", "c", iso(1), nil))
	s.Load(wire.Snapshot{
		Agents:    []wire.Agent{mkAgent(aID, nil)},
		OpenCalls: []wire.Call{mkCall(aID, "c9", nil)},
		Files:     []wire.FileTouch{{Path: "/p/f", LastTouchAt: iso(0), Attribution: "unattributed"}},
	})
	if _, ok := s.Agent("old"); ok {
		t.Fatal("agent old survived Load")
	}
	if _, ok := s.Agent(aID); !ok {
		t.Fatal("agent missing after Load")
	}
	equal(t, callIDs(s.Snapshot(base).OpenCalls), []string{"c9"})
	forwarded(t, s, start(mkCall("old", "c", nil)))
	dropped(t, s, start(mkCall(aID, "c9", nil))) // still open
}

// ended-call memory is a bounded LRU of 20,000 keys
func TestEndedCallMemoryIsBoundedLRU(t *testing.T) {
	s := newState()
	for i := 0; i < 20_000; i++ {
		forwarded(t, s, end(aID, fmt.Sprintf("k%d", i), iso(0), nil))
	}
	dropped(t, s, end(aID, "k0", iso(0), nil))      // still remembered (and now most recent)
	forwarded(t, s, end(aID, "extra", iso(0), nil)) // evicts k1, the oldest
	dropped(t, s, end(aID, "k0", iso(0), nil))
	forwarded(t, s, end(aID, "k1", iso(0), nil))
}

// decodeGolden reads the source events of a golden file with the wire decoder.
func decodeGolden(t *testing.T, path string) []wire.SourceEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []wire.SourceEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		se, err := wire.DecodeSourceEvent([]byte(line))
		if err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if _, raw := se.Ev.(wire.Raw); raw {
			t.Fatalf("golden file holds an unknown kind %q", se.Ev.Kind())
		}
		out = append(out, se)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// snapshot + later deltas equals full replay (golden file)
func TestSnapshotPlusDeltasEqualsFullReplay(t *testing.T) {
	events := decodeGolden(t, "../../test/fixtures/golden/session.ndjson")
	if len(events) < 10 {
		t.Fatalf("golden file has %d events, want at least 10", len(events))
	}
	now := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC).UnixMilli()
	half := len(events) / 2

	full := newState()
	for _, e := range events {
		full.Apply(e)
	}
	expected := full.Snapshot(now)
	if len(expected.Agents) != 2 || len(expected.OpenCalls) == 0 || len(expected.Files) == 0 {
		t.Fatalf("full snapshot = %#v", expected)
	}

	first := newState()
	for _, e := range events[:half] {
		first.Apply(e)
	}
	mid := first.Snapshot(now)
	if len(mid.OpenCalls) == 0 {
		t.Fatal("the cut must fall while a call is open")
	}
	raw, err := json.Marshal(mid)
	if err != nil {
		t.Fatal(err)
	}
	var loaded wire.Snapshot
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	second := newState()
	second.Load(loaded)
	for _, e := range events[half:] {
		second.Apply(e)
	}
	equal(t, second.Snapshot(now), expected)

	// Hand-written fixture rules hold in the replay.
	find := func(path string) *wire.FileTouch {
		for i := range expected.Files {
			if expected.Files[i].Path == path {
				return &expected.Files[i]
			}
		}
		return nil
	}
	if find("/work/proj/e.ts") != nil {
		t.Fatal("search file e.ts was touched")
	}
	if f := find("/work/proj/a.ts"); f == nil || f.Attribution != "unattributed" {
		t.Fatalf("a.ts = %#v", f)
	}
	if f := find("/work/proj/new.ts"); f == nil || f.Attribution != "confirmed" {
		t.Fatalf("new.ts = %#v", f)
	}
}

// watchRoots: projectRoots of present agents, newest first, max 24, never home or /
func TestWatchRoots(t *testing.T) {
	s := newState()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string, root *string, ageMs int64) {
		forwarded(t, s, upsert(mkAgent(id, func(a *wire.Agent) { a.ProjectRoot, a.LastEventAt = root, iso(-ageMs) })))
	}
	mk("a", sp("/r/a"), 3_000)
	mk("b", sp("/r/b"), 1_000)
	mk("b2", sp("/r/b"), 500)
	mk("c", sp("/r/c"), 2_000)
	mk("home", sp(home), 100)
	mk("slash", sp("/"), 100)
	mk("none", nil, 100)
	mk("expired", sp("/r/expired"), 700_000)
	equal(t, s.WatchRoots(base, 24), []string{"/r/b", "/r/c", "/r/a"})

	many := newState()
	for i := range 30 {
		forwarded(t, many, upsert(mkAgent(fmt.Sprintf("m%d", i), func(a *wire.Agent) {
			a.ProjectRoot, a.LastEventAt = sp(fmt.Sprintf("/m/%d", i)), iso(-int64(i)*1_000)
		})))
	}
	roots := many.WatchRoots(base, 24)
	equal(t, len(roots), 24)
	equal(t, roots[0], "/m/0")
	equal(t, roots[23], "/m/23")
	equal(t, many.WatchRoots(base, 3), []string{"/m/0", "/m/1", "/m/2"})
}

// custom lifecycle options apply to snapshot and tick
func TestCustomLifecycleOptions(t *testing.T) {
	s := NewState(lifecycle.Options{LingerMs: 1_000, CallTimeoutMs: 2_000})
	forwarded(t, s, upsert(mkAgent(aID, nil)))
	forwarded(t, s, start(mkCall(aID, "c", nil)))
	equal(t, len(s.Snapshot(base+999).Agents), 1)
	equal(t, len(s.Snapshot(base+1_000).Agents), 0)
	equal(t, len(s.Tick(base+2_000)), 1)
}

// load: an open call counts its agent last event as its own, so a mirror tick does not end a call the collector still runs
func TestLoadOpenCallCountsAgentLastEvent(t *testing.T) {
	s := newState()
	s.Load(wire.Snapshot{
		Agents: []wire.Agent{mkAgent(aID, func(a *wire.Agent) { a.Status, a.LastEventAt = "working", iso(899_000) })},
		OpenCalls: []wire.Call{mkCall(aID, "long", func(c *wire.Call) {
			c.Category, c.RawName, c.StartedAt = "command", "Bash", iso(0)
		})},
	})
	// 15 minutes after the start, 1 second after the agent's last event.
	equal(t, len(s.Tick(base+900_000)), 0)
	equal(t, callIDs(s.Snapshot(base+900_000).OpenCalls), []string{"long"})
	// With no later event, the call ends 10 minutes after the agent's last event.
	equal(t, len(s.Tick(base+899_000+600_000)), 1)
}
