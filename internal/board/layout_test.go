package board

import (
	"slices"
	"testing"
	"time"

	"agent-view/internal/schema"
	"agent-view/internal/store"
)

var nowMs = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UnixMilli()

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

func ptr[T any](v T) *T { return &v }

var seq int64

func env(host string, e schema.Envelope, ts int64) schema.Envelope {
	seq++
	e.V, e.Host, e.Seq, e.Ts = 1, host, seq, iso(ts)
	return e
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.New(store.Options{Now: func() int64 { return nowMs }})
	t.Cleanup(func() {
		if s.Panics != 0 {
			t.Errorf("Apply recovered %d panic(s)", s.Panics)
		}
	})
	return s
}

// addAgent upserts an agent. parent "" makes a main agent; link applies to
// sub-agents.
func addAgent(s *store.Store, host, id, parent, link string) {
	a := schema.Agent{
		ID: id, Provider: "claude", Name: id, Role: "main", Link: "root", Cwd: ptr("/p"),
		ProjectRoot: ptr("/p"), Status: "idle", LastEventAt: iso(nowMs),
	}
	if parent != "" {
		a.ParentID, a.Role, a.Link = ptr(parent), "sub-agent", link
	}
	s.Apply(env(host, schema.Envelope{Kind: "agent.upsert", Agent: &a}, nowMs))
}

func startCall(s *store.Store, host, agentID, callID string, at int64) {
	c := schema.Call{AgentID: agentID, CallID: callID, StartedAt: iso(at), Category: "read", RawName: "Read"}
	s.Apply(env(host, schema.Envelope{Kind: "tool.start", Call: &c}, at))
}

func endCall(s *store.Store, host, agentID, callID string, at int64) {
	s.Apply(env(host, schema.Envelope{Kind: "tool.end", AgentID: agentID, CallID: callID, Ok: ptr(true)}, at))
}

func rowFor(t *testing.T, rows []Row, id string) Row {
	t.Helper()
	for _, r := range rows {
		if r.AgentID == id {
			return r
		}
	}
	t.Fatalf("no row for %q", id)
	return Row{}
}

// hk returns the key of an agent on host "h"; "" gives the zero key, none.
func hk(id string) store.AgentKey {
	if id == "" {
		return store.AgentKey{}
	}
	return store.AgentKey{Host: "h", ID: id}
}

// rowAt returns the row of an agent key.
func rowAt(t *testing.T, rows []Row, k store.AgentKey) Row {
	t.Helper()
	for _, r := range rows {
		if r.Key() == k {
			return r
		}
	}
	t.Fatalf("no row for %v", k)
	return Row{}
}

// TestRowsParentOnAnotherHostIsNoParent: a parent ID that exists only on
// another host is no parent. With the parent on both hosts, the child is a
// sub-row of the parent on its own host only. A selection on one twin makes
// only that row full.
func TestRowsParentOnAnotherHostIsNoParent(t *testing.T) {
	s := newStore(t)
	addAgent(s, "a", "p", "", "")
	addAgent(s, "b", "c", "p", "confirmed")
	rows := Rows(s, UI{}, nowMs)
	if c := rowAt(t, rows, store.AgentKey{Host: "b", ID: "c"}); c.Sub || c.LastSib {
		t.Errorf("c on b with p only on a: Sub/LastSib = %v/%v, want false/false", c.Sub, c.LastSib)
	}

	s = newStore(t)
	addAgent(s, "a", "p", "", "")
	addAgent(s, "b", "p", "", "")
	addAgent(s, "b", "c", "p", "confirmed")
	rows = Rows(s, UI{}, nowMs)
	var order []store.AgentKey
	for _, r := range rows {
		order = append(order, r.Key())
	}
	want := []store.AgentKey{{Host: "a", ID: "p"}, {Host: "b", ID: "p"}, {Host: "b", ID: "c"}}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if c := rows[2]; !c.Sub || !c.LastSib {
		t.Errorf("c on b: Sub/LastSib = %v/%v, want true/true", c.Sub, c.LastSib)
	}
	for _, i := range []int{0, 1} {
		if rows[i].Sub {
			t.Errorf("%v: Sub = true, want false", rows[i].Key())
		}
	}

	rows = Rows(s, UI{Selected: store.AgentKey{Host: "b", ID: "p"}}, nowMs)
	if r := rowAt(t, rows, store.AgentKey{Host: "b", ID: "p"}); !r.Full {
		t.Errorf("selected b/p: Full = false, want true")
	}
	if r := rowAt(t, rows, store.AgentKey{Host: "a", ID: "p"}); r.Full {
		t.Errorf("twin a/p of the selection: Full = true, want false")
	}
	rows = Rows(s, UI{Focus: store.AgentKey{Host: "a", ID: "p"}}, nowMs)
	if r := rowAt(t, rows, store.AgentKey{Host: "a", ID: "p"}); !r.Full {
		t.Errorf("focused a/p: Full = false, want true")
	}
	if r := rowAt(t, rows, store.AgentKey{Host: "b", ID: "p"}); r.Full {
		t.Errorf("twin b/p of the focus: Full = true, want false")
	}
}

func TestRowsFullRule(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"open", "fresh", "edge", "old", "sel", "foc", "none"} {
		addAgent(s, "h", id, "", "")
	}
	startCall(s, "h", "open", "c1", nowMs-5_000)
	for _, c := range []struct {
		id  string
		ago int64
	}{{"fresh", 30_000}, {"edge", 60_000}, {"old", 90_000}} {
		startCall(s, "h", c.id, "c", nowMs-c.ago-1_000)
		endCall(s, "h", c.id, "c", nowMs-c.ago)
	}

	rows := Rows(s, UI{Selected: hk("sel"), Focus: hk("foc")}, nowMs)
	want := map[string]bool{
		"open": true, "fresh": true, "edge": true, "old": false, "sel": true, "foc": true, "none": false,
	}
	for id, full := range want {
		r := rowFor(t, rows, id)
		if r.Full != full {
			t.Errorf("%s: Full = %v, want %v", id, r.Full, full)
		}
		wantH := 1
		if full {
			wantH = 9
		}
		if r.H != wantH {
			t.Errorf("%s: H = %d, want %d", id, r.H, wantH)
		}
	}
	y := 0
	for _, r := range rows {
		if r.Y != y {
			t.Errorf("%s: Y = %d, want %d", r.AgentID, r.Y, y)
		}
		y += r.H
	}
}

func TestRowsHostHeaders(t *testing.T) {
	s := newStore(t)
	addAgent(s, "alpha", "a1", "", "")
	addAgent(s, "alpha", "a2", "", "")
	addAgent(s, "beta", "b1", "", "")
	rows := Rows(s, UI{}, nowMs)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	headers := []string{rows[0].HostHeader, rows[1].HostHeader, rows[2].HostHeader}
	if want := []string{"alpha", "", "beta"}; !slices.Equal(headers, want) {
		t.Errorf("headers = %q, want %q", headers, want)
	}
	if rows[0].H != 2 || rows[1].H != 1 || rows[2].H != 2 {
		t.Errorf("H = %d %d %d, want 2 1 2", rows[0].H, rows[1].H, rows[2].H)
	}
	if rows[1].Y != 2 || rows[2].Y != 3 {
		t.Errorf("Y = %d %d, want 2 3", rows[1].Y, rows[2].Y)
	}
	if len(Gutter(rows, 0)) != 2 {
		t.Errorf("gutter lines = %d, want 2", len(Gutter(rows, 0)))
	}

	one := newStore(t)
	addAgent(one, "alpha", "a1", "", "")
	addAgent(one, "alpha", "a2", "", "")
	for _, r := range Rows(one, UI{}, nowMs) {
		if r.HostHeader != "" {
			t.Errorf("one host: header %q on %s", r.HostHeader, r.AgentID)
		}
	}
}

func TestRowsSubFlags(t *testing.T) {
	s := newStore(t)
	addAgent(s, "h", "p", "", "")
	addAgent(s, "h", "k1", "p", "confirmed")
	addAgent(s, "h", "k2", "p", "session")
	addAgent(s, "h", "orphan", "gone", "confirmed")
	rows := Rows(s, UI{}, nowMs)
	var order []string
	for _, r := range rows {
		order = append(order, r.AgentID)
	}
	if want := []string{"p", "k1", "k2", "orphan"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	check := func(id string, sub, last, session bool) {
		t.Helper()
		r := rowFor(t, rows, id)
		if r.Sub != sub || r.LastSib != last || r.SessionLink != session {
			t.Errorf("%s: Sub/LastSib/SessionLink = %v/%v/%v, want %v/%v/%v",
				id, r.Sub, r.LastSib, r.SessionLink, sub, last, session)
		}
	}
	check("p", false, false, false)
	check("k1", true, false, false)
	check("k2", true, true, true)
	check("orphan", false, false, false)
}

func TestHistoryCount(t *testing.T) {
	for _, c := range []struct {
		width int
		sub   bool
		want  int
	}{{80, false, 1}, {120, false, 4}, {200, false, 6}, {80, true, 1}, {58, false, 0}, {10, true, 0}} {
		if got := HistoryCount(c.width, c.sub); got != c.want {
			t.Errorf("HistoryCount(%d, %v) = %d, want %d", c.width, c.sub, got, c.want)
		}
	}
}

func TestScrollKeepsSelection(t *testing.T) {
	rows := make([]Row, 10)
	for i := range rows {
		rows[i] = Row{AgentID: string(rune('a' + i)), Host: "h", Full: true, Y: i * 9, H: 9}
	}
	top := Scroll(rows, 30, 0, hk("h")) // row 7
	r := rows[7]
	if r.Y < top || r.Y+r.H > top+30 {
		t.Errorf("top = %d does not hold row 7 (%d..%d)", top, r.Y, r.Y+r.H)
	}
	for _, i := range []int{5, 6, 7} {
		if rows[i].Y < top || rows[i].Y+rows[i].H > top+30 {
			t.Errorf("row %d not fully visible at top %d", i, top)
		}
	}
	if got := Scroll(rows, 30, 17, hk("")); got != 17 {
		t.Errorf("no selection: top = %d, want 17", got)
	}
	if got := Scroll(rows, 30, 17, hk("zz")); got != 17 {
		t.Errorf("unknown selection: top = %d, want 17", got)
	}
	if got := Scroll(rows, 30, 63, hk("b")); got != 9 { // row 1 above the view
		t.Errorf("scroll up: top = %d, want 9", got)
	}
	if got := Scroll(rows, 30, 40, hk("f")); got != 40 { // 45..54 already visible
		t.Errorf("already visible: top = %d, want 40", got)
	}
	if got := Scroll(rows, 5, 0, hk("c")); got != 18 { // taller than height: align top
		t.Errorf("tall row: top = %d, want 18", got)
	}
}

func subRows(n int, link string) []Row {
	rows := []Row{{AgentID: "p", Full: true, Y: 0, H: 9}}
	y := 9
	for i := 0; i < n; i++ {
		rows = append(rows, Row{
			AgentID: "k", Sub: true, LastSib: i == n-1, SessionLink: link == "session",
			Full: true, Y: y, H: 9,
		})
		y += 9
	}
	return rows
}

func TestGutters(t *testing.T) {
	v := "  │"
	for n := 1; n <= 3; n++ {
		rows := subRows(n, "confirmed")
		// The main row: nothing above, a line below its card band.
		wantMain := []string{"", "", "", "", "", "", "", v, v}
		if got := Gutter(rows, 0); !slices.Equal(got, wantMain) {
			t.Errorf("n=%d main = %q, want %q", n, got, wantMain)
		}
		for i := 1; i <= n; i++ {
			got := Gutter(rows, i)
			if len(got) != 9 {
				t.Fatalf("n=%d row %d: %d lines, want 9", n, i, len(got))
			}
			last := i == n
			tee, below := "  ├──", v
			if last {
				tee, below = "  └──", ""
			}
			want := []string{v, v, v, tee, below, below, below, below, below}
			if !slices.Equal(got, want) {
				t.Errorf("n=%d row %d = %q, want %q", n, i, got, want)
			}
		}
	}
}

func TestGuttersSessionLink(t *testing.T) {
	d := "  ┆"
	rows := subRows(2, "session")
	want := []string{d, d, d, "  ├──", d, d, d, d, d}
	if got := Gutter(rows, 1); !slices.Equal(got, want) {
		t.Errorf("first = %q, want %q", got, want)
	}
	want = []string{d, d, d, "  └──", "", "", "", "", ""}
	if got := Gutter(rows, 2); !slices.Equal(got, want) {
		t.Errorf("last = %q, want %q", got, want)
	}
	// The main row's line follows the first child's link.
	if got := Gutter(rows, 0); got[7] != d || got[8] != d {
		t.Errorf("main below = %q, want dashed", got[7:])
	}
}

func TestGuttersCollapsedAndHeader(t *testing.T) {
	rows := []Row{
		{AgentID: "p", Full: true, Y: 0, H: 9},
		{AgentID: "k1", Sub: true, Y: 9, H: 1},
		{AgentID: "k2", Sub: true, LastSib: true, Y: 10, H: 1},
		{AgentID: "q", Full: false, HostHeader: "beta", Y: 11, H: 2},
	}
	if got := Gutter(rows, 1); !slices.Equal(got, []string{"  ├──"}) {
		t.Errorf("collapsed mid = %q", got)
	}
	if got := Gutter(rows, 2); !slices.Equal(got, []string{"  └──"}) {
		t.Errorf("collapsed last = %q", got)
	}
	if got := Gutter(rows, 3); !slices.Equal(got, []string{"", ""}) {
		t.Errorf("collapsed main with header = %q", got)
	}
	if got := Gutter(rows, 9); got != nil {
		t.Errorf("out of range = %q, want nil", got)
	}
	// A collapsed main row has no band to extend, even above a sub-agent.
	rows = []Row{{AgentID: "p", H: 1}, {AgentID: "k", Sub: true, LastSib: true, Y: 1, H: 1}}
	if got := Gutter(rows, 0); !slices.Equal(got, []string{""}) {
		t.Errorf("collapsed main = %q", got)
	}
	// A header on a full sub row (unusual) leaves the header line empty.
	rows = []Row{{AgentID: "k", Sub: true, LastSib: true, Full: true, HostHeader: "x", H: 10}}
	got := Gutter(rows, 0)
	if len(got) != 10 || got[0] != "" || got[4] != "  └──" {
		t.Errorf("header sub = %q", got)
	}
}

// runCommand starts a command with the given output; end > 0 ends it then.
func runCommand(s *store.Store, agentID, callID, output string, start, end int64) {
	c := schema.Call{AgentID: agentID, CallID: callID, StartedAt: iso(start), Category: "command", RawName: "Bash", Command: "make"}
	s.Apply(env("h", schema.Envelope{Kind: "tool.start", Call: &c}, start))
	s.Apply(env("h", schema.Envelope{Kind: "tool.output", AgentID: agentID, CallID: callID, Chunk: output}, start))
	if end > 0 {
		s.Apply(env("h", schema.Envelope{Kind: "tool.end", AgentID: agentID, CallID: callID, Ok: ptr(true)}, end))
	}
}

func TestRowsFocusedCommandGrows(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		addAgent(s, "h", id, "", "")
	}
	twelve := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	runCommand(s, "a", "c1", twelve, nowMs-1_000, 0)
	runCommand(s, "b", "c2", "x\ny\nz\n", nowMs-2_000, nowMs-1_000) // ended: the shown call
	runCommand(s, "c", "c3", "x\ny\n", nowMs-1_000, 0)
	startCall(s, "h", "d", "c4", nowMs-1_000)

	for _, tc := range []struct {
		focus string
		want  int
	}{{"a", 17}, {"b", 10}, {"c", 9}, {"d", 9}, {"", 9}} {
		rows := Rows(s, UI{Focus: hk(tc.focus)}, nowMs)
		for i, r := range rows {
			want := 9
			if r.Key() == hk(tc.focus) {
				want = tc.want
			}
			if r.H != want {
				t.Errorf("focus %q: row %s H = %d, want %d", tc.focus, r.AgentID, r.H, want)
			}
			if i > 0 && r.Y != rows[i-1].Y+rows[i-1].H {
				t.Errorf("focus %q: row %s Y = %d, previous ends at %d", tc.focus, r.AgentID, r.Y, rows[i-1].Y+rows[i-1].H)
			}
		}
	}

	rows := Rows(s, UI{Focus: hk("a"), Selected: hk("b")}, nowMs)
	if b := rowFor(t, rows, "b"); b.Y != 17 {
		t.Fatalf("b.Y = %d, want 17", b.Y)
	}
	if top := Scroll(rows, 10, 0, hk("b")); top != 17+9-10 {
		t.Errorf("Scroll to b = %d, want %d", top, 17+9-10)
	}
}
