package anim

import (
	"reflect"
	"testing"

	"agent-view/internal/store"
)

func started(agent string) store.Change {
	return store.Change{Kind: store.CallStarted, AgentID: agent, CallID: "c"}
}

func steps(e *Engine, n int) {
	for i := 0; i < n; i++ {
		e.Step()
	}
}

func TestCallPrintsOneLinePerFrame(t *testing.T) {
	e := New(false)
	e.OnChange(started("a"), true)
	if got := e.Frame("a", 9, 0).CallRows; got != 0 {
		t.Fatalf("CallRows = %d, want 0", got)
	}
	if !e.Active() {
		t.Fatal("not active after start")
	}
	steps(e, 3)
	if got := e.Frame("a", 9, 0).CallRows; got != 3 {
		t.Fatalf("CallRows = %d, want 3", got)
	}
	steps(e, 4)
	if got := e.Frame("a", 9, 0).CallRows; got != 7 {
		t.Fatalf("CallRows = %d, want 7", got)
	}
	steps(e, 5) // let the other effects of a start finish
	if e.Active() {
		t.Fatal("still active")
	}
}

func TestStartCollapsesLeavingCardAndPrintsHistory(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 3)
	e.OnChange(started("a"), true)
	f := e.Frame("a", 9, 3)
	if f.LeavingRows != 7 || f.HistPrintRows != 0 || f.WhereChars != 0 {
		t.Fatalf("start frame = %+v", f)
	}
	var leaving, hist []int
	for i := 0; i < 4; i++ {
		e.Step()
		f = e.Frame("a", 9, 3)
		leaving = append(leaving, f.LeavingRows)
		hist = append(hist, f.HistPrintRows)
	}
	if !reflect.DeepEqual(leaving, []int{6, 4, 2, 0}) {
		t.Fatalf("leaving = %v", leaving)
	}
	if !reflect.DeepEqual(hist, []int{1, 2, 3, 4}) {
		t.Fatalf("hist = %v", hist)
	}
}

func TestWhereTypesOutOverSixFrames(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 0)
	e.OnChange(started("a"), true)
	for want := 0; want <= 6; want++ {
		if got := e.Frame("a", 9, 0).WhereChars; got != want {
			t.Fatalf("frame %d: WhereChars = %d", want, got)
		}
		e.Step()
	}
	if got := e.Frame("a", 9, 0).WhereChars; got != 6 {
		t.Fatalf("WhereChars = %d, want 6", got)
	}
}

func TestFailFlashes3Times(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 0)
	e.OnChange(store.Change{Kind: store.CallEnded, AgentID: "a", CallID: "c"}, false)
	if e.Active() {
		t.Fatal("CallEnded alone must not animate")
	}
	e.OnFail("a")
	var got []bool
	for i := 0; i < 6; i++ {
		got = append(got, e.Frame("a", 9, 0).Flash)
		if !e.Active() {
			t.Fatalf("inactive at frame %d", i)
		}
		e.Step()
	}
	want := []bool{true, false, true, false, true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flash = %v, want %v", got, want)
	}
	if e.Frame("a", 9, 0).Flash || e.Active() {
		t.Fatal("flash did not end")
	}
}

func TestHistoryShiftsBy3(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 4)
	e.OnChange(started("a"), true)
	var got []int
	for i := 0; i < 6; i++ {
		f := e.Frame("a", 9, 4)
		if len(f.HistOffset) != 4 {
			t.Fatalf("len = %d", len(f.HistOffset))
		}
		if f.HistOffset[0] != 0 {
			t.Fatalf("index 0 offset = %d", f.HistOffset[0])
		}
		if f.HistOffset[3] != f.HistOffset[1] {
			t.Fatalf("offsets differ: %v", f.HistOffset)
		}
		got = append(got, f.HistOffset[1])
		e.Step()
	}
	if want := []int{-15, -12, -9, -6, -3, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("offsets = %v, want %v", got, want)
	}
}

func TestHistoryChangesDoNotAnimate(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 2)
	for _, k := range []store.ChangeKind{store.CallStarted, store.CallEnded, store.AgentAdded, store.AgentStatus, store.CallOutput} {
		e.OnChange(store.Change{Kind: k, AgentID: "a", History: true}, true)
	}
	if e.Active() {
		t.Fatal("history change animated")
	}
	f := e.Frame("a", 9, 2)
	if f.CallRows != 7 || f.LeavingRows != 0 || f.HistPrintRows != 4 || f.WhereChars != 6 {
		t.Fatalf("frame = %+v", f)
	}
}

func TestBurstAnimatesOnlyNewest(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 3)
	for i := 0; i < 5; i++ {
		e.OnChange(started("a"), true)
	}
	if got := e.Frame("a", 9, 3).CallRows; got != 0 {
		t.Fatalf("CallRows = %d, want 0", got)
	}
	n := 0
	for e.Active() {
		e.Step()
		n++
		if n > 100 {
			t.Fatal("never settles")
		}
	}
	// one sequence: the 7-frame print is the longest effect
	if n != 7 {
		t.Fatalf("settled after %d steps, want 7", n)
	}
}

func TestParallelStartBehindShownCallDoesNotAnimate(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 3)
	e.OnChange(started("a"), false)
	if e.Active() {
		t.Fatal("parallel start animated")
	}
	f := e.Frame("a", 9, 3)
	if f.CallRows != 7 || f.LeavingRows != 0 || f.HistPrintRows != 4 || f.WhereChars != 6 {
		t.Fatalf("frame = %+v", f)
	}
	for _, o := range f.HistOffset {
		if o != 0 {
			t.Fatalf("offsets = %v", f.HistOffset)
		}
	}
}

func TestReducedMotionIsInstant(t *testing.T) {
	e := New(true)
	e.Frame("a", 9, 2)
	e.OnChange(started("a"), true)
	e.OnChange(store.Change{Kind: store.AgentAdded, AgentID: "b"}, true)
	e.OnAgentAdded("c", true)
	e.OnFail("a")
	if e.Active() {
		t.Fatal("active in reduced mode")
	}
	f := e.Frame("a", 9, 2)
	want := AgentFrame{CallRows: 7, HistPrintRows: 4, WhereChars: 6, GutterRows: 9, RowH: 9, HistOffset: []int{0, 0}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("frame = %+v, want %+v", f, want)
	}
	if g := e.Frame("c", 1, 0); g.GutterRows != 1 || g.RowH != 1 || g.CallRows != 7 {
		t.Fatalf("frame c = %+v", g)
	}
	if f := e.Frame("a", 1, 0); f.RowH != 1 {
		t.Fatalf("RowH = %d, want 1", f.RowH)
	}
}

func TestSubAgentGutterThenRowPrint(t *testing.T) {
	e := New(false)
	e.OnAgentAdded("s", true)
	// first sight at target: no row-height animation
	f := e.Frame("s", 3, 0)
	if f.GutterRows != 0 || f.CallRows != 0 || f.RowH != 3 {
		t.Fatalf("start = %+v", f)
	}
	for want := 1; want <= 3; want++ {
		e.Step()
		f = e.Frame("s", 3, 0)
		if f.GutterRows != want || f.CallRows != 0 {
			t.Fatalf("gutter step %d: %+v", want, f)
		}
	}
	for want := 1; want <= 7; want++ {
		e.Step()
		f = e.Frame("s", 3, 0)
		if f.GutterRows != 3 || f.CallRows != want {
			t.Fatalf("print step %d: %+v", want, f)
		}
	}
	if e.Active() {
		t.Fatal("still active")
	}
}

func TestAgentAddedWithoutParentPrintsRowOnly(t *testing.T) {
	e := New(false)
	e.OnChange(store.Change{Kind: store.AgentAdded, AgentID: "m"}, false)
	f := e.Frame("m", 9, 0)
	if f.GutterRows != 9 || f.CallRows != 0 {
		t.Fatalf("frame = %+v", f)
	}
	e.Step()
	if got := e.Frame("m", 9, 0).CallRows; got != 1 {
		t.Fatalf("CallRows = %d, want 1", got)
	}
	e2 := New(false)
	e2.OnAgentAdded("m", false)
	if f := e2.Frame("m", 9, 0); f.GutterRows != 9 || f.CallRows != 0 {
		t.Fatalf("OnAgentAdded(false) frame = %+v", f)
	}
}

func TestRowHeightMovesOneLinePerFrame(t *testing.T) {
	e := New(false)
	if got := e.Frame("a", 9, 0).RowH; got != 9 {
		t.Fatalf("first RowH = %d, want 9", got)
	}
	if e.Active() {
		t.Fatal("first sight animated")
	}
	if got := e.Frame("a", 1, 0).RowH; got != 9 {
		t.Fatalf("RowH = %d, want 9 before step", got)
	}
	if !e.Active() {
		t.Fatal("not active while collapsing")
	}
	for want := 8; want >= 1; want-- {
		e.Step()
		if got := e.Frame("a", 1, 0).RowH; got != want {
			t.Fatalf("RowH = %d, want %d", got, want)
		}
	}
	if e.Active() {
		t.Fatal("still active")
	}
	e.Frame("a", 4, 0)
	e.Step()
	if got := e.Frame("a", 4, 0).RowH; got != 2 {
		t.Fatalf("RowH = %d, want 2", got)
	}
}

func TestForgetDropsAgentState(t *testing.T) {
	e := New(false)
	e.Frame("a", 9, 0)
	e.OnChange(started("a"), true)
	if !e.Active() {
		t.Fatal("not active")
	}
	e.Forget("a")
	if e.Active() {
		t.Fatal("active after Forget")
	}
	if f := e.Frame("a", 5, 0); f.CallRows != 7 || f.RowH != 5 {
		t.Fatalf("frame = %+v", f)
	}
	e.Forget("missing") // no panic
}
