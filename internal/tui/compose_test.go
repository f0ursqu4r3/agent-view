package tui

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-view/internal/anim"
	"agent-view/internal/board"
	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// Clock text depends on the zone, so the tests run in UTC.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

var nowMs = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UnixMilli()

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

func ptr[T any](v T) *T { return &v }

// fixture is a store on host "devbox" with a settable clock. Every applied
// change goes to the log.
type fixture struct {
	s     *store.Store
	log   *board.Log
	clock int64
	seq   int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{log: &board.Log{}, clock: nowMs}
	f.s = store.New(store.Options{Now: func() int64 { return f.clock }})
	t.Cleanup(func() {
		if f.s.Panics != 0 {
			t.Errorf("Apply recovered %d panic(s)", f.s.Panics)
		}
	})
	return f
}

func (f *fixture) apply(e schema.Envelope) []store.Change {
	f.seq++
	e.V, e.Host, e.Seq, e.Ts = 1, "devbox", f.seq, iso(f.clock)
	ch := f.s.Apply(e)
	for _, c := range ch {
		f.log.FromChange(f.s, c)
	}
	return ch
}

// agent upserts a working Claude agent in /work/<repo>. parent "" makes a
// main agent.
func (f *fixture) agent(id, parent string) {
	a := schema.Agent{
		ID: id, Provider: "claude", Name: "agent " + id, Role: "main", Link: "root",
		Cwd: ptr("/work/repo"), ProjectRoot: ptr("/work/repo"), Status: "working", LastEventAt: iso(f.clock),
	}
	if parent != "" {
		a.ParentID, a.Role, a.Link = ptr(parent), "sub-agent", "confirmed"
	}
	f.apply(schema.Envelope{Kind: "agent.upsert", Agent: &a})
}

func (f *fixture) start(agentID, callID, cat, file string) []store.Change {
	c := schema.Call{AgentID: agentID, CallID: callID, StartedAt: iso(f.clock), Category: cat, RawName: "Tool", Files: []string{file}}
	return f.apply(schema.Envelope{Kind: "tool.start", Call: &c})
}

func (f *fixture) end(agentID, callID string, ok bool) []store.Change {
	return f.apply(schema.Envelope{Kind: "tool.end", AgentID: agentID, CallID: callID, Ok: ptr(ok)})
}

func (f *fixture) input(w, h int) Input {
	return Input{
		Store: f.s, Anim: anim.New(false), Log: f.log, Now: f.clock, W: w, H: h,
		Status: Status{Hosts: []HostStatus{{Label: "devbox", State: "up"}}, Agents: 1, Clock: "01:00:00"},
	}
}

func plainLines(g *cells.Grid) []string { return strings.Split(cells.Plain(g), "\n") }

// at returns the text of the cells x..x+w-1 on row y.
func at(g *cells.Grid, x, y, w int) string {
	var b strings.Builder
	for i := x; i < x+w && i < g.W; i++ {
		b.WriteString(g.C[y*g.W+i].G)
	}
	return b.String()
}

func hitFor(t *testing.T, hits []Hit, id string) Hit {
	t.Helper()
	for _, h := range hits {
		if h.AgentID == id {
			return h
		}
	}
	t.Fatalf("no hit for %q in %+v", id, hits)
	return Hit{}
}

func TestComposeTooSmall(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	for _, sz := range [][2]int{{79, 23}, {79, 40}, {120, 23}} {
		g, hits := Compose(f.input(sz[0], sz[1]))
		if g.W != sz[0] || g.H != sz[1] {
			t.Fatalf("%v: grid is %d×%d", sz, g.W, g.H)
		}
		if p := cells.Plain(g); !strings.Contains(p, "too small") || strings.Contains(p, "AGENT VIEW") {
			t.Errorf("%v: want only the too-small screen:\n%s", sz, p)
		}
		if len(hits) != 0 {
			t.Errorf("%v: hits = %+v", sz, hits)
		}
	}
	for _, in := range []Input{{}, {W: -3, H: -1}, {W: 5, H: 1}} {
		g, hits := Compose(in)
		if g.W != max(in.W, 0) || g.H != max(in.H, 0) || len(hits) != 0 {
			t.Errorf("%d×%d: grid %d×%d, %d hits", in.W, in.H, g.W, g.H, len(hits))
		}
	}
}

func TestComposeStatusAndKeys(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	in := f.input(80, 24)
	in.Status.Hosts[0].Label = "dev\x1b[31mbox"
	g, _ := Compose(in)
	lines := plainLines(g)
	first, last := lines[0], lines[len(lines)-1]
	for _, want := range []string{"AGENT VIEW", "devbox", "up", "1 agent", "01:00:00"} {
		if !strings.Contains(first, want) {
			t.Errorf("status bar lacks %q: %q", want, first)
		}
	}
	if !strings.Contains(last, "q quit") {
		t.Errorf("key bar lacks %q: %q", "q quit", last)
	}

	in.UI.Paused, in.Status.Paused = true, true
	in.Status.Hosts[0].Degraded = 2
	g, _ = Compose(in)
	lines = plainLines(g)
	for _, want := range []string{"PAUSED", "2 sources degraded"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("paused status bar lacks %q: %q", want, lines[0])
		}
	}
	if !strings.Contains(lines[len(lines)-1], "space resume") {
		t.Errorf("paused key bar: %q", lines[len(lines)-1])
	}
}

func TestComposeEmptyBoard(t *testing.T) {
	f := newFixture(t)
	g, hits := Compose(f.input(80, 24))
	if !strings.Contains(plainLines(g)[2], "waiting for agents…") {
		t.Errorf("empty board:\n%s", cells.Plain(g))
	}
	if len(hits) != 0 {
		t.Errorf("hits = %+v", hits)
	}
}

// loadDemo replays testdata/feed/demo.ndjson with the store clock at each
// envelope's time, as the feed would deliver it, and returns the store, the
// log, the time of the last envelope, and the live call starts.
func loadDemo(t *testing.T) (*store.Store, *board.Log, int64, []int64) {
	t.Helper()
	fh, err := os.Open(filepath.Join("..", "..", "testdata", "feed", "demo.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var clock int64
	s := store.New(store.Options{Now: func() int64 { return clock }})
	log := &board.Log{}
	var starts []int64
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		e, ok := schema.DecodeLine(sc.Bytes())
		if !ok {
			t.Fatalf("bad line: %s", sc.Text())
		}
		ts, err := time.Parse(time.RFC3339Nano, e.Ts)
		if err != nil {
			t.Fatal(err)
		}
		clock = ts.UnixMilli()
		for _, c := range s.Apply(e) {
			log.FromChange(s, c)
			if c.Kind == store.CallStarted && !c.History {
				starts = append(starts, c.AtMs)
			}
		}
		log.AddTouches(s.TakeUnattributed(clock), clock)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if s.Panics != 0 {
		t.Fatalf("Apply recovered %d panic(s)", s.Panics)
	}
	return s, log, clock, starts
}

func TestComposeDemoSnapshot(t *testing.T) {
	s, log, last, starts := loadDemo(t)
	now := last + 100
	st := Status{Hosts: []HostStatus{{Label: "demo", State: "up"}}, Clock: time.UnixMilli(now).Format("15:04:05")}
	for _, a := range s.Agents(now) {
		st.Agents++
		if a.Status == "working" {
			st.Working++
		}
		st.Faults += s.Counters(a.ID).Faults
	}
	for _, at := range starts {
		if now-at <= 60_000 {
			st.CallsPerMin++
		}
	}
	// The board is taller than the view; a top past the end clamps to the
	// last screenful.
	for _, c := range []struct {
		name string
		top  int
	}{{"demo", 0}, {"demo_scrolled", 1000}} {
		in := Input{Store: s, Anim: anim.New(false), Log: log, Status: st, Now: now, W: 120, H: 40}
		in.UI.Top = c.top
		g, _ := Compose(in)
		checkGolden(t, c.name, cells.Plain(g)+"\n")
	}
}

// checkGolden compares got with testdata/compose/<name>.golden at the
// repository root; -update rewrites the file.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "compose", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden.\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

func TestComposeFocusDims(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	f.agent("b", "")
	f.start("a", "c1", "read", "/work/repo/src/a.go")
	f.start("b", "c2", "edit", "/work/repo/src/b.go")
	f.clock += 1500
	f.end("b", "c2", true)
	in := f.input(120, 40)
	in.UI.Focus = "a"
	g, hits := Compose(in)
	masks := strings.Split(cells.Mask(g), "\n")

	hb := hitFor(t, hits, "b")
	for y := hb.Y; y < hb.Y+hb.H; y++ {
		if strings.Trim(masks[y], "df") != "" {
			t.Errorf("row of b, line %d not dim: %s", y, masks[y])
		}
	}
	ha := hitFor(t, hits, "a")
	if strings.Trim(masks[ha.Y], "df") == "" {
		t.Errorf("focused row of a is dim: %s", masks[ha.Y])
	}
}

func TestComposeHitsMatchCards(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	f.agent("s", "a")
	f.agent("c", "")
	f.start("a", "c1", "read", "/work/repo/src/a.go")
	f.start("s", "c2", "edit", "/work/repo/src/s.go")
	g, hits := Compose(f.input(120, 40))
	if len(hits) != 3 {
		t.Fatalf("hits = %+v", hits)
	}
	for _, c := range []struct {
		id, corner string
		w, h       int
	}{{"a", "┏", 24, 7}, {"s", "╭", 24, 7}, {"c", "▸", 0, 1}} {
		h := hitFor(t, hits, c.id)
		if got := at(g, h.X, h.Y, 1); got != c.corner {
			t.Errorf("%s: cell at hit (%d,%d) = %q, want %q", c.id, h.X, h.Y, got, c.corner)
		}
		if c.w != 0 && h.W != c.w || h.H != c.h {
			t.Errorf("%s: hit size %d×%d", c.id, h.W, h.H)
		}
		if line := at(g, h.X, h.Y, g.W-h.X); !strings.Contains(line, "agent "+c.id) {
			t.Errorf("%s: hit line lacks the name: %q", c.id, line)
		}
	}
	if hs, ha := hitFor(t, hits, "s"), hitFor(t, hits, "a"); hs.X != ha.X+5 || hs.Y != ha.Y+9 {
		t.Errorf("sub-agent card at (%d,%d), parent at (%d,%d)", hs.X, hs.Y, ha.X, ha.Y)
	}
	if got := at(g, hitFor(t, hits, "s").X-4, hitFor(t, hits, "s").Y+3, 4); got != " └──" {
		t.Errorf("sub-agent gutter = %q", got)
	}

	// The board fits, so any top clamps to 0.
	base := cells.Plain(g)
	for _, top := range []int{-5, 3, 1000} {
		in := f.input(120, 40)
		in.UI.Top = top
		g2, hits2 := Compose(in)
		if cells.Plain(g2) != base || len(hits2) != 3 {
			t.Errorf("top %d not clamped", top)
		}
	}

	// A short screen scrolls: the hits follow the board and leave out
	// cards above the view.
	in := f.input(80, 24)
	in.UI.Top = 9
	g3, hits3 := Compose(in)
	if len(hits3) != 2 {
		t.Fatalf("scrolled hits = %+v", hits3)
	}
	hs := hitFor(t, hits3, "s")
	if hs.Y != 2 || at(g3, hs.X, hs.Y, 1) != "╭" {
		t.Errorf("scrolled sub-agent hit = %+v", hs)
	}
}

func TestComposePanels(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	f.apply(schema.Envelope{Kind: "coverage", Coverage: &schema.Coverage{
		LogRoots: []string{"/home/u/.claude/projects"}, LogsOpen: 2, LogsDiscovered: 5, MalformedRecords: 3,
		Sources: map[string]schema.SourceState{"claude": {State: "ok"}, "codex": {State: "degraded", Detail: "no\x1b[31m logs"}},
	}})
	inner := func(g *cells.Grid) string {
		l := plainLines(g)
		return strings.Join(l[1:len(l)-1], "\n")
	}

	in := f.input(100, 30)
	in.UI.Panel = board.PanelCoverage
	in.Diagnostics = []string{"exec: bun: not found"}
	g, hits := Compose(in)
	p := inner(g)
	for _, want := range []string{"coverage", "malformed", "3", "2 of 5 discovered", "/home/u/.claude/projects", "codex", "degraded", "no logs", "exec: bun: not found"} {
		if !strings.Contains(p, want) {
			t.Errorf("coverage panel lacks %q:\n%s", want, p)
		}
	}
	if len(hits) != 0 {
		t.Errorf("hits under a panel = %+v", hits)
	}

	in.UI.Panel = board.PanelHelp
	g, _ = Compose(in)
	p = inner(g)
	for _, want := range []string{"keys", "space", "pause", "enter", "focus", "esc", "quit"} {
		if !strings.Contains(p, want) {
			t.Errorf("help panel lacks %q:\n%s", want, p)
		}
	}
}

// TestComposeAnimation checks that the composer applies the frame values: a
// new call card prints over the leaving one, the "where" line types out, and
// a failure flashes the card.
func TestComposeAnimation(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	f.start("a", "c1", "read", "/work/repo/src/old.go")
	f.clock += 1000
	f.end("a", "c1", true)
	e := anim.New(false)
	in := f.input(120, 40)
	in.Anim = e
	g, hits := Compose(in) // first sight: done values
	ha := hitFor(t, hits, "a")
	cx, cy := ha.X+25, ha.Y
	if got := at(g, cx, cy+1, 28); !strings.Contains(got, "old.go") {
		t.Fatalf("shown card: %q", got)
	}

	f.clock += 1000
	for _, c := range f.start("a", "c2", "edit", "/work/repo/lib/new.go") {
		e.OnChange(c, true)
	}
	in = f.input(120, 40)
	in.Anim = e
	g, _ = Compose(in)
	if got := at(g, cx, cy+1, 28); !strings.Contains(got, "old.go") {
		t.Errorf("frame 0: leaving card not drawn: %q", got)
	}
	if got := at(g, cx, cy+7, 40); strings.TrimSpace(got) != "" {
		t.Errorf("frame 0: where line = %q", got)
	}
	for i := 0; i < 7; i++ {
		e.Step()
	}
	g, _ = Compose(in)
	if got := at(g, cx, cy+1, 28); !strings.Contains(got, "new.go") {
		t.Errorf("frame 7: new card = %q", got)
	}
	if got := at(g, cx, cy+7, 40); !strings.HasPrefix(got, "repo:lib/ $ ▌") {
		t.Errorf("frame 7: where line = %q", got)
	}
	if got := at(g, cx+29, cy+3, 14); !strings.Contains(got, "READ") {
		t.Errorf("history card = %q", got)
	}

	e.OnFail("a")
	g, _ = Compose(in)
	mask := strings.Split(cells.Mask(g), "\n")
	if got := mask[cy+2][cx : cx+28]; strings.Trim(got, "#") != "" {
		t.Errorf("flash frame not inverse: %s", got)
	}
}

// TestComposeTallRows checks rows taller than the board's: the focused
// command card grows to 10 output lines and its row grows with it, and a row
// that collapses keeps its height for a frame with the tree line drawn on.
func TestComposeTallRows(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	f.agent("s1", "a")
	f.agent("s2", "a")
	c := schema.Call{AgentID: "a", CallID: "c1", StartedAt: iso(f.clock), Category: "command", RawName: "Bash", Command: "make"}
	f.apply(schema.Envelope{Kind: "tool.start", Call: &c})
	f.apply(schema.Envelope{Kind: "tool.output", AgentID: "a", CallID: "c1", Chunk: "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12\n"})
	f.start("s1", "c2", "read", "/work/repo/x.go")
	f.start("s2", "c3", "read", "/work/repo/y.go")
	e := anim.New(false)
	in := f.input(120, 60)
	in.Anim, in.UI.Focus = e, "a"
	g, hits := Compose(in)
	ha, hs1 := hitFor(t, hits, "a"), hitFor(t, hits, "s1")
	if hs1.Y != ha.Y+17 {
		t.Fatalf("s1 at line %d, a at %d: want a's row 17 high", hs1.Y, ha.Y)
	}
	cx := ha.X + 25
	if got := at(g, cx, ha.Y+3, 28); !strings.Contains(got, "> l3") {
		t.Errorf("first output line = %q", got)
	}
	if got := at(g, cx, ha.Y+12, 28); !strings.Contains(got, "> l12") {
		t.Errorf("last output line = %q", got)
	}
	for y := ha.Y + 7; y < hs1.Y; y++ {
		if got := at(g, 3, y, 1); got != "│" {
			t.Errorf("line %d: gutter %q, want │", y, got)
		}
	}

	// s1 ends and goes idle past 60 s: it collapses, one line per frame.
	f.clock += 1000
	f.end("s1", "c2", true)
	f.clock += 61_000
	f.end("s2", "c3", true) // keeps s2 full
	in = f.input(120, 60)
	in.Anim, in.UI.Focus = e, "a"
	g, hits = Compose(in)
	hs1, hs2 := hitFor(t, hits, "s1"), hitFor(t, hits, "s2")
	if hs1.H != 1 || hs2.Y != hs1.Y+9 {
		t.Fatalf("collapsing s1 = %+v, s2 = %+v", hs1, hs2)
	}
	if got := at(g, 3, hs1.Y, 3); got != "├──" {
		t.Errorf("collapsing s1 tee = %q", got)
	}
	for y := hs1.Y + 1; y < hs2.Y; y++ {
		if got := at(g, 3, y, 1); got != "│" {
			t.Errorf("line %d: gutter %q, want │", y, got)
		}
	}
	for i := 0; i < 8; i++ {
		e.Step()
	}
	_, hits = Compose(in)
	if hs1, hs2 := hitFor(t, hits, "s1"), hitFor(t, hits, "s2"); hs2.Y != hs1.Y+1 {
		t.Errorf("collapsed s1 = %+v, s2 = %+v", hs1, hs2)
	}
}
