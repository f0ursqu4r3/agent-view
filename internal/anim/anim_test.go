package anim

import (
	"reflect"
	"testing"

	"agent-view/internal/store"
)

func started(agent string) store.Change {
	return store.Change{Kind: store.CallStarted, Host: "h", AgentID: agent, CallID: "c"}
}

// k returns the key of an agent on host "h".
func k(id string) store.AgentKey { return store.AgentKey{Host: "h", ID: id} }

func steps(e *Engine, n int) {
	for i := 0; i < n; i++ {
		e.Step()
	}
}

func TestCallPrintsOneLinePerFrame(t *testing.T) {
	e := New(false)
	e.OnChange(started("a"), true)
	if got := e.Frame(k("a"), 9, 0).CallRows; got != 0 {
		t.Fatalf("CallRows = %d, want 0", got)
	}
	if !e.Active() {
		t.Fatal("not active after start")
	}
	steps(e, 3)
	if got := e.Frame(k("a"), 9, 0).CallRows; got != 3 {
		t.Fatalf("CallRows = %d, want 3", got)
	}
	steps(e, 4)
	if got := e.Frame(k("a"), 9, 0).CallRows; got != 7 {
		t.Fatalf("CallRows = %d, want 7", got)
	}
	steps(e, 5) // let the other effects of a start finish
	if e.Active() {
		t.Fatal("still active")
	}
}

func TestStartCollapsesLeavingCardAndPrintsHistory(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 9, 3)
	e.OnChange(started("a"), true)
	f := e.Frame(k("a"), 9, 3)
	if f.LeavingRows != 7 || f.HistPrintRows != 0 || f.WhereChars != 0 {
		t.Fatalf("start frame = %+v", f)
	}
	var leaving, hist []int
	for i := 0; i < 4; i++ {
		e.Step()
		f = e.Frame(k("a"), 9, 3)
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
	e.Frame(k("a"), 9, 0)
	e.OnChange(started("a"), true)
	for want := 0; want <= 6; want++ {
		if got := e.Frame(k("a"), 9, 0).WhereChars; got != want {
			t.Fatalf("frame %d: WhereChars = %d", want, got)
		}
		e.Step()
	}
	if got := e.Frame(k("a"), 9, 0).WhereChars; got != 6 {
		t.Fatalf("WhereChars = %d, want 6", got)
	}
}

func TestFailFlashes3Times(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 9, 0)
	e.OnChange(store.Change{Kind: store.CallEnded, Host: "h", AgentID: "a", CallID: "c"}, false)
	if e.Active() {
		t.Fatal("CallEnded alone must not animate")
	}
	e.OnFail(k("a"))
	var got []bool
	for i := 0; i < 6; i++ {
		got = append(got, e.Frame(k("a"), 9, 0).Flash)
		if !e.Active() {
			t.Fatalf("inactive at frame %d", i)
		}
		e.Step()
	}
	want := []bool{true, false, true, false, true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flash = %v, want %v", got, want)
	}
	if e.Frame(k("a"), 9, 0).Flash || e.Active() {
		t.Fatal("flash did not end")
	}
}

// An interrupt flashes the STOPPED word once (one frame on, one off); the
// call card does not flash.
func TestStopFlashesOnce(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 9, 0)
	e.OnStopped(k("a"))
	var stop, card []bool
	for i := 0; i < 2; i++ {
		f := e.Frame(k("a"), 9, 0)
		stop, card = append(stop, f.StopFlash), append(card, f.Flash)
		if !e.Active() {
			t.Fatalf("inactive at frame %d", i)
		}
		e.Step()
	}
	if want := []bool{true, false}; !reflect.DeepEqual(stop, want) {
		t.Fatalf("StopFlash = %v, want %v", stop, want)
	}
	if want := []bool{false, false}; !reflect.DeepEqual(card, want) {
		t.Fatalf("Flash = %v, want %v: the card must not flash", card, want)
	}
	if f := e.Frame(k("a"), 9, 0); f.StopFlash || e.Active() {
		t.Fatal("stop flash did not end")
	}
	// Reduced motion: no flash.
	r := New(true)
	r.OnStopped(k("a"))
	if r.Frame(k("a"), 9, 0).StopFlash || r.Active() {
		t.Fatal("reduced motion flashed")
	}
}

func TestHistoryShiftsBy3(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 9, 4)
	e.OnChange(started("a"), true)
	var got []int
	for i := 0; i < 6; i++ {
		f := e.Frame(k("a"), 9, 4)
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
	e.Frame(k("a"), 9, 2)
	for _, k := range []store.ChangeKind{store.CallStarted, store.CallEnded, store.AgentAdded, store.AgentStatus, store.CallOutput} {
		e.OnChange(store.Change{Kind: k, Host: "h", AgentID: "a", History: true}, true)
	}
	if e.Active() {
		t.Fatal("history change animated")
	}
	f := e.Frame(k("a"), 9, 2)
	if f.CallRows != 7 || f.LeavingRows != 0 || f.HistPrintRows != 4 || f.WhereChars != 6 {
		t.Fatalf("frame = %+v", f)
	}
}

func TestBurstAnimatesOnlyNewest(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 9, 3)
	for i := 0; i < 5; i++ {
		e.OnChange(started("a"), true)
	}
	if got := e.Frame(k("a"), 9, 3).CallRows; got != 0 {
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
	e.Frame(k("a"), 9, 3)
	e.OnChange(started("a"), false)
	if e.Active() {
		t.Fatal("parallel start animated")
	}
	f := e.Frame(k("a"), 9, 3)
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
	e.Frame(k("a"), 9, 2)
	e.OnChange(started("a"), true)
	e.OnChange(store.Change{Kind: store.AgentAdded, Host: "h", AgentID: "b"}, true)
	e.OnAgentAdded(k("c"), true)
	e.OnFail(k("a"))
	if e.Active() {
		t.Fatal("active in reduced mode")
	}
	f := e.Frame(k("a"), 9, 2)
	want := AgentFrame{CallRows: 7, HistPrintRows: 4, WhereChars: 6, GutterRows: 9, RowH: 9, HistOffset: []int{0, 0}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("frame = %+v, want %+v", f, want)
	}
	if g := e.Frame(k("c"), 1, 0); g.GutterRows != 1 || g.RowH != 1 || g.CallRows != 7 {
		t.Fatalf("frame c = %+v", g)
	}
	if f := e.Frame(k("a"), 1, 0); f.RowH != 1 {
		t.Fatalf("RowH = %d, want 1", f.RowH)
	}
}

func TestSubAgentGutterThenRowPrint(t *testing.T) {
	e := New(false)
	e.OnAgentAdded(k("s"), true)
	// first sight at target: no row-height animation
	f := e.Frame(k("s"), 3, 0)
	if f.GutterRows != 0 || f.CallRows != 0 || f.RowH != 3 {
		t.Fatalf("start = %+v", f)
	}
	for want := 1; want <= 3; want++ {
		e.Step()
		f = e.Frame(k("s"), 3, 0)
		if f.GutterRows != want || f.CallRows != 0 {
			t.Fatalf("gutter step %d: %+v", want, f)
		}
	}
	for want := 1; want <= 7; want++ {
		e.Step()
		f = e.Frame(k("s"), 3, 0)
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
	e.OnChange(store.Change{Kind: store.AgentAdded, Host: "h", AgentID: "m"}, false)
	f := e.Frame(k("m"), 9, 0)
	if f.GutterRows != 9 || f.CallRows != 0 {
		t.Fatalf("frame = %+v", f)
	}
	e.Step()
	if got := e.Frame(k("m"), 9, 0).CallRows; got != 1 {
		t.Fatalf("CallRows = %d, want 1", got)
	}
	e2 := New(false)
	e2.OnAgentAdded(k("m"), false)
	if f := e2.Frame(k("m"), 9, 0); f.GutterRows != 9 || f.CallRows != 0 {
		t.Fatalf("OnAgentAdded(false) frame = %+v", f)
	}
}

func TestRowHeightMovesOneLinePerFrame(t *testing.T) {
	e := New(false)
	if got := e.Frame(k("a"), 9, 0).RowH; got != 9 {
		t.Fatalf("first RowH = %d, want 9", got)
	}
	if e.Active() {
		t.Fatal("first sight animated")
	}
	if got := e.Frame(k("a"), 1, 0).RowH; got != 9 {
		t.Fatalf("RowH = %d, want 9 before step", got)
	}
	if !e.Active() {
		t.Fatal("not active while collapsing")
	}
	for want := 8; want >= 1; want-- {
		e.Step()
		if got := e.Frame(k("a"), 1, 0).RowH; got != want {
			t.Fatalf("RowH = %d, want %d", got, want)
		}
	}
	if e.Active() {
		t.Fatal("still active")
	}
	e.Frame(k("a"), 4, 0)
	e.Step()
	if got := e.Frame(k("a"), 4, 0).RowH; got != 2 {
		t.Fatalf("RowH = %d, want 2", got)
	}
}

func TestForgetDropsAgentState(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 9, 0)
	e.OnChange(started("a"), true)
	if !e.Active() {
		t.Fatal("not active")
	}
	e.Forget(k("a"))
	if e.Active() {
		t.Fatal("active after Forget")
	}
	if f := e.Frame(k("a"), 5, 0); f.CallRows != 7 || f.RowH != 5 {
		t.Fatalf("frame = %+v", f)
	}
	e.Forget(k("missing")) // no panic
}

// TestSettleSnapsRowHeight checks that Settle makes the next Frame jump to
// its target height and finish a drawing gutter, while the other effects
// of the agent keep running.
func TestSettleSnapsRowHeight(t *testing.T) {
	e := New(false)
	e.Frame(k("a"), 1, 0) // seen collapsed
	e.OnChange(started("a"), true)
	e.OnFail(k("a"))
	e.Settle(k("a"))
	f := e.Frame(k("a"), 9, 2)
	if f.RowH != 9 {
		t.Errorf("settled RowH = %d, want 9", f.RowH)
	}
	if f.CallRows != 0 || !f.Flash || f.WhereChars != 0 || f.HistOffset[1] != -15 {
		t.Errorf("settle cancelled live effects: %+v", f)
	}
	steps(e, 1)
	if f := e.Frame(k("a"), 9, 2); f.RowH != 9 || f.CallRows != 1 {
		t.Errorf("after a step: %+v", f)
	}

	// Settle is used up by one Frame: a later target change animates.
	steps(e, 10)
	if e.Active() {
		t.Fatal("still active")
	}
	if f := e.Frame(k("a"), 1, 0); f.RowH != 9 || !e.Active() {
		t.Errorf("later collapse: RowH %d, active %v; want 9 and animating", f.RowH, e.Active())
	}

	// A drawing gutter completes.
	e.OnAgentAdded(k("s"), true)
	if got := e.Frame(k("s"), 9, 0).GutterRows; got != 0 {
		t.Fatalf("gutter = %d, want 0", got)
	}
	e.Settle(k("s"))
	if f := e.Frame(k("s"), 9, 0); f.GutterRows != 9 || f.CallRows != 0 {
		t.Errorf("settled gutter: %+v; want gutter 9, row print running", f)
	}

	// No state: nothing to settle, and none is made.
	e.Settle(k("x"))
	if _, ok := e.agents[k("x")]; ok {
		t.Errorf("Settle made state for an unknown agent")
	}
	r := New(true)
	r.Settle(k("a"))
	if r.Active() {
		t.Errorf("reduced engine active after Settle")
	}
}

// TestEqualIDsOnTwoHostsAnimateApart: two hosts run an agent with the same ID.
// The appearance of one starts no effect on the other, and Forget of one
// keeps the state of the other.
func TestEqualIDsOnTwoHostsAnimateApart(t *testing.T) {
	one := store.AgentKey{Host: "box", ID: "s1"}
	two := store.AgentKey{Host: "box#2", ID: "s1"}
	e := New(false)
	e.OnAgentAdded(one, true)
	if f := e.Frame(two, 9, 0); f.GutterRows != 9 || f.CallRows != 7 {
		t.Fatalf("twin of an added agent: %+v, want no appearance", f)
	}
	if f := e.Frame(one, 9, 0); f.GutterRows != 0 || f.CallRows != 0 {
		t.Fatalf("added agent: %+v, want the gutter and the print to start", f)
	}

	e.OnChange(store.Change{Kind: store.CallStarted, Host: two.Host, AgentID: two.ID, CallID: "c"}, true)
	e.OnFail(two)
	e.Forget(one)
	if f := e.Frame(two, 9, 0); f.CallRows != 0 || !f.Flash || f.WhereChars != 0 {
		t.Fatalf("twin after Forget of the other host: %+v, want its effects kept", f)
	}
	if f := e.Frame(one, 9, 0); f.GutterRows != 9 || f.CallRows != 7 || f.Flash {
		t.Fatalf("forgotten agent: %+v, want done values", f)
	}
}
