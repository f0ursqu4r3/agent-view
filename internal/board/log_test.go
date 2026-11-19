package board

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

// Clock text depends on the zone, so the tests run in UTC.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

// runCall starts and ends a call of a category on host "h" and returns the
// change of the end. ok nil leaves the result unknown.
func runCall(s *store.Store, agentID, callID, cat string, files []string, cmd string, ok *bool, start, end int64) store.Change {
	c := schema.Call{AgentID: agentID, CallID: callID, StartedAt: iso(start), Category: cat, RawName: "Tool", Files: files, Command: cmd}
	s.Apply(env("h", schema.Envelope{Kind: "tool.start", Call: &c}, start))
	for _, ch := range s.Apply(env("h", schema.Envelope{Kind: "tool.end", AgentID: agentID, CallID: callID, Ok: ok}, end)) {
		if ch.Kind == store.CallEnded {
			return ch
		}
	}
	panic("no CallEnded change")
}

func TestLogSkipsHistory(t *testing.T) {
	s := newStore(t)
	addAgent(s, "h", "a", "", "")
	ch := runCall(s, "a", "c1", "read", []string{"/p/x.go"}, "", ptr(true), nowMs-100, nowMs)
	var l Log
	ch.History = true
	l.FromChange(s, ch)
	if l.Len() != 0 {
		t.Fatalf("history change wrote %d line(s)", l.Len())
	}
	ch.History = false
	l.FromChange(s, ch)
	if l.Len() != 1 {
		t.Fatalf("live change wrote %d line(s), want 1", l.Len())
	}
}

func TestLogToolLine(t *testing.T) {
	s := newStore(t)
	addAgent(s, "h", "a", "", "")
	var l Log
	l.FromChange(s, runCall(s, "a", "c1", "command", nil, "make test", ptr(false), nowMs-1400, nowMs))
	// A call timeout (ok null) comes long after the call's last activity, the
	// agent's last event here: the duration runs to that activity.
	l.FromChange(s, runCall(s, "a", "c2", "read", []string{"/p/README.md"}, "", nil, nowMs-500, nowMs+600_000))
	got := cells.Plain(l.Render(80, 6, ""))
	for _, want := range []string{"RUN", "$ make test", "FAIL", "1.4s", "READ", "README.md", "??", "0.5s", "01:00:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("Plain lacks %q:\n%s", want, got)
		}
	}
	mask := strings.Split(cells.Mask(l.Render(80, 6, "")), "\n")
	if !strings.Contains(mask[2], "#") {
		t.Errorf("FAIL cell is not inverse:\n%s", strings.Join(mask, "\n"))
	}
}

func TestLogToolLineFolder(t *testing.T) {
	s := newStore(t)
	addAgent(s, "h", "a", "", "")
	var l Log
	l.FromChange(s, runCall(s, "a", "c1", "read", []string{"/p/viewer/stage/tree-view.ts"}, "", ptr(true), nowMs-10, nowMs))
	if got := cells.Plain(l.Render(80, 5, "")); !strings.Contains(got, "viewer/stage/tree-view.ts") {
		t.Errorf("folder missing:\n%s", got)
	}
	// Too narrow for the folder: the base name stays.
	if got := cells.Plain(l.Render(70, 5, "")); !strings.Contains(got, " tree-view.ts") {
		t.Errorf("base name missing:\n%s", got)
	}
}

func TestLogBootLine(t *testing.T) {
	s := newStore(t)
	s.Apply(env("h", schema.Envelope{Kind: "agent.upsert", Agent: &schema.Agent{
		ID: "p", Provider: "claude", Name: "agent-view", Role: "main", Link: "root", Status: "idle", LastEventAt: iso(nowMs),
	}}, nowMs))
	a := schema.Agent{ID: "s", Provider: "claude", Name: "reviewer", Role: "sub-agent", ParentID: ptr("p"), Link: "confirmed", Status: "working", LastEventAt: iso(nowMs)}
	var l Log
	for _, ch := range s.Apply(env("h", schema.Envelope{Kind: "agent.upsert", Agent: &a}, nowMs)) {
		l.FromChange(s, ch)
	}
	got := cells.Plain(l.Render(80, 5, ""))
	for _, want := range []string{"BOOT", "sub-agent of agent-view", "reviewer"} {
		if !strings.Contains(got, want) {
			t.Errorf("Plain lacks %q:\n%s", want, got)
		}
	}
	// A new main agent writes no line.
	n := l.Len()
	for _, ch := range s.Apply(env("h", schema.Envelope{Kind: "agent.upsert", Agent: &schema.Agent{ID: "m", Provider: "codex", Name: "m", Role: "main", Link: "root", Status: "idle", LastEventAt: iso(nowMs)}}, nowMs)) {
		l.FromChange(s, ch)
	}
	if l.Len() != n {
		t.Errorf("main agent added a line")
	}
}

func TestLogStatusLines(t *testing.T) {
	s := newStore(t)
	addAgent(s, "h", "a", "", "")
	var l Log
	for _, st := range []string{"working", "interrupted", "finished"} {
		l.FromChange(s, store.Change{Kind: store.AgentStatus, Host: "h", AgentID: "a", Status: st, AtMs: nowMs})
	}
	got := cells.Plain(l.Render(80, 6, ""))
	if !strings.Contains(got, "STOP") || !strings.Contains(got, "DONE") || l.Len() != 2 {
		t.Errorf("want STOP and DONE only (len %d):\n%s", l.Len(), got)
	}
}

func TestLogHostLine(t *testing.T) {
	var l Log
	l.Host(nowMs, "box", "loaded 3 agents, 40 calls from logs")
	got := cells.Plain(l.Render(80, 4, ""))
	if !strings.Contains(got, "HOST  box loaded 3 agents, 40 calls from logs") {
		t.Errorf("host line wrong:\n%s", got)
	}
}

func TestLogGroupsTouches(t *testing.T) {
	var l Log
	base := int64(1_700_000_000_000)
	l.AddTouches([]store.Touch{
		{Host: "h", Path: "/p/src/a.go", AtMs: base},
		{Host: "h", Path: "/p/src/b.go", AtMs: base + 300},
		{Host: "h", Path: "/p/src/c.go", AtMs: base + 900},
		{Host: "h", Path: "/p/test/t.go", AtMs: base + 400},
		{Host: "h", Path: "/p/old/o.go", AtMs: base - 10_001}, // history
	}, base+1000)
	got := cells.Plain(l.Render(80, 6, ""))
	for _, want := range []string{"FS  3 change(s) in /p/src/", "FS  1 change(s) in /p/test/"} {
		if !strings.Contains(got, want) {
			t.Errorf("Plain lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/p/old/") || l.Len() != 2 {
		t.Errorf("history touch shown (len %d):\n%s", l.Len(), got)
	}
	// Another bucket and another host are other groups.
	l.AddTouches([]store.Touch{
		{Host: "h", Path: "/p/src/a.go", AtMs: base + 2500},
		{Host: "g", Path: "/p/src/a.go", AtMs: base + 2500},
	}, base+3000)
	if l.Len() != 4 {
		t.Errorf("len %d, want 4", l.Len())
	}
	// FS lines are dim and have no who.
	for _, ln := range l.lines {
		if !ln.Dim || ln.Who != "" {
			t.Errorf("FS line not dim or has who: %+v", ln)
		}
	}
}

// TestLogMergesTouchesAcrossCalls checks that one group (host, folder,
// 2-second bucket) released in two sweeps stays one FS line.
func TestLogMergesTouchesAcrossCalls(t *testing.T) {
	var l Log
	base := int64(1_700_000_000_000)
	l.AddTouches([]store.Touch{{Host: "h", Path: "/p/src/a.go", AtMs: base + 100}}, base+2200)
	l.Add(LogLine{AtMs: base + 2300, AgentID: "a", Who: "agent-a", Label: "READ", Target: "x.go", Result: "ok"})
	l.AddTouches([]store.Touch{
		{Host: "h", Path: "/p/src/b.go", AtMs: base + 1500},
		{Host: "h", Path: "/p/src/c.go", AtMs: base + 1900},
		{Host: "g", Path: "/p/src/a.go", AtMs: base + 1500}, // another host
		{Host: "h", Path: "/p/src/d.go", AtMs: base + 2100}, // another bucket
	}, base+4000)
	if l.Len() != 4 {
		t.Fatalf("len %d, want 4 (merged FS line, READ, host g, next bucket)", l.Len())
	}
	if got := l.lines[0]; got.Target != "3 change(s) in /p/src/" || got.AtMs != base+1900 {
		t.Errorf("merged line = %q at %d, want 3 changes at %d", got.Target, got.AtMs, base+1900)
	}
	for i, want := range []string{"x.go", "1 change(s) in /p/src/", "1 change(s) in /p/src/"} {
		if got := l.lines[i+1].Target; got != want {
			t.Errorf("line %d = %q, want %q", i+1, got, want)
		}
	}
}

func TestLogRingAndFocus(t *testing.T) {
	var l Log
	for i := range 600 {
		who := "a"
		if i%2 == 1 {
			who = "b"
		}
		l.Add(LogLine{AtMs: int64(i) * 1000, AgentID: who, Who: "agent-" + who, Label: "READ", Target: fmt.Sprintf("f%d", i), Result: "ok"})
	}
	if l.Len() != 500 {
		t.Fatalf("len %d, want 500", l.Len())
	}
	if l.lines[0].Target != "f100" {
		t.Errorf("oldest %q, want f100", l.lines[0].Target)
	}
	l.Host(700_000, "box", "up")

	all := cells.Plain(l.Render(80, 12, ""))
	if !strings.Contains(all, "agent-a") || !strings.Contains(all, "agent-b") {
		t.Errorf("unfocused log lacks an agent:\n%s", all)
	}
	foc := cells.Plain(l.Render(80, 12, "a"))
	if strings.Contains(foc, "agent-b") || !strings.Contains(foc, "agent-a") || !strings.Contains(foc, "HOST  box up") {
		t.Errorf("focus a wrong:\n%s", foc)
	}
}

func TestLogRenderBox(t *testing.T) {
	var l Log
	for _, size := range [][2]int{{80, 9}, {100, 12}, {40, 5}, {6, 3}, {3, 2}} {
		g := l.Render(size[0], size[1], "")
		if g.W != size[0] || g.H != size[1] {
			t.Errorf("size %v: got %dx%d", size, g.W, g.H)
		}
	}
	rows := strings.Split(cells.Plain(l.Render(60, 6, "")), "\n")
	if len(rows) != 6 || !strings.Contains(rows[0], "events · tail -f") || !strings.Contains(rows[4], "$ ▌") {
		t.Errorf("box wrong:\n%s", strings.Join(rows, "\n"))
	}
	// Lines sit above the prompt, newest last, empty lines above.
	l.Add(LogLine{AtMs: 1000, AgentID: "a", Who: "a", Label: "READ", Target: "one", Result: "ok"})
	l.Add(LogLine{AtMs: 2000, AgentID: "a", Who: "a", Label: "READ", Target: "two", Result: "ok"})
	rows = strings.Split(cells.Plain(l.Render(60, 6, "")), "\n")
	if !strings.Contains(rows[2], "one") || !strings.Contains(rows[3], "two") {
		t.Errorf("order wrong:\n%s", strings.Join(rows, "\n"))
	}
	// Control characters in names never reach the grid.
	l.Add(LogLine{AtMs: 3000, AgentID: "a", Who: "x\x1b[31my", Label: "READ", Target: "t", Result: "ok"})
	if strings.Contains(cells.Plain(l.Render(60, 6, "")), "\x1b") {
		t.Errorf("escape reached the grid")
	}
}
