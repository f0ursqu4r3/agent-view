package lifecycle

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"agent-view/internal/wire"
)

var now = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UnixMilli()

func ago(ms int64) string {
	return time.UnixMilli(now - ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

func str(s string) *string { return &s }

func agent(id string, mod func(*wire.Agent)) wire.Agent {
	a := wire.Agent{
		ID: id, Provider: "claude", Name: id, Role: "main", Link: "root",
		Cwd: str("/p"), ProjectRoot: str("/p"), Status: "idle", LastEventAt: ago(0),
	}
	if mod != nil {
		mod(&a)
	}
	return a
}

func sortedIDs(m map[string]bool) []string {
	out := []string{}
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// LIFECYCLE holds the spec values
func TestDefaultsHoldTheSpecValues(t *testing.T) {
	want := Options{LingerMs: 600_000, StaleMs: 120_000, FinishedFadeMs: 60_000, CallTimeoutMs: 600_000, FileTTLMs: 1_800_000}
	if got := Defaults(); got != want {
		t.Fatalf("Defaults() = %+v, want %+v", got, want)
	}
	if got := (Options{StaleMs: 10_000}).WithDefaults(); got.StaleMs != 10_000 || got.LingerMs != 600_000 || got.FileTTLMs != 1_800_000 {
		t.Fatalf("WithDefaults() = %+v", got)
	}
}

// present: agent with event inside linger; ancestors of present agents; finished only for 1 minute
func TestPresentLingerAncestorsFinished(t *testing.T) {
	agents := []wire.Agent{
		agent("fresh", func(a *wire.Agent) { a.LastEventAt = ago(599_999) }),
		agent("old", func(a *wire.Agent) { a.LastEventAt = ago(600_000) }),
		agent("root-old", func(a *wire.Agent) { a.LastEventAt = ago(3_600_000) }),
		agent("mid-old", func(a *wire.Agent) { a.ParentID = str("root-old"); a.LastEventAt = ago(3_000_000) }),
		agent("leaf", func(a *wire.Agent) { a.ParentID = str("mid-old"); a.Role = "sub-agent"; a.LastEventAt = ago(1_000) }),
		agent("fin-new", func(a *wire.Agent) { a.Status = "finished"; a.LastEventAt = ago(59_999) }),
		agent("fin-old", func(a *wire.Agent) { a.Status = "finished"; a.LastEventAt = ago(60_000) }),
		agent("orphan", func(a *wire.Agent) { a.ParentID = str("missing"); a.LastEventAt = ago(1_000) }),
	}
	got := sortedIDs(PresentAgentIDs(agents, now, Options{}))
	want := []string{"fin-new", "fresh", "leaf", "mid-old", "orphan", "root-old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("present = %v, want %v", got, want)
	}
}

// present: options override linger; parent cycles do not hang
func TestPresentOptionsOverrideLingerCyclesDoNotHang(t *testing.T) {
	agents := []wire.Agent{
		agent("a", func(a *wire.Agent) { a.ParentID = str("b"); a.LastEventAt = ago(1_000) }),
		agent("b", func(a *wire.Agent) { a.ParentID = str("a"); a.LastEventAt = ago(9_000_000) }),
		agent("c", func(a *wire.Agent) { a.LastEventAt = ago(5_000) }),
	}
	if got, want := sortedIDs(PresentAgentIDs(agents, now, Options{LingerMs: 2_000})), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with linger 2s: %v, want %v", got, want)
	}
	if got, want := sortedIDs(PresentAgentIDs(agents, now, Options{})), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with defaults: %v, want %v", got, want)
	}
}

// isStale only for working agents with no event for 2 minutes
func TestIsStaleOnlyWorkingAgentsIdleForTwoMinutes(t *testing.T) {
	cases := []struct {
		name string
		a    wire.Agent
		o    Options
		want bool
	}{
		{"working 120001", agent("a", func(a *wire.Agent) { a.Status = "working"; a.LastEventAt = ago(120_001) }), Options{}, true},
		{"working 120000", agent("a", func(a *wire.Agent) { a.Status = "working"; a.LastEventAt = ago(120_000) }), Options{}, false},
		{"idle 500000", agent("a", func(a *wire.Agent) { a.Status = "idle"; a.LastEventAt = ago(500_000) }), Options{}, false},
		{"working 30000 stale 10000", agent("a", func(a *wire.Agent) { a.Status = "working"; a.LastEventAt = ago(30_000) }), Options{StaleMs: 10_000}, true},
	}
	for _, c := range cases {
		if got := IsStale(c.a.Status, MsOf(c.a.LastEventAt), now, c.o); got != c.want {
			t.Errorf("%s: IsStale = %v, want %v", c.name, got, c.want)
		}
	}
}

// laterISO returns b only when its time is greater; an empty time loses
func TestLaterISO(t *testing.T) {
	const jan1, jan2 = "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"
	if got := LaterISO("", jan1); got != jan1 {
		t.Errorf("empty first: %q", got)
	}
	if got := LaterISO(jan2, jan1); got != jan2 {
		t.Errorf("first later: %q", got)
	}
	if got := LaterISO(jan1, jan2); got != jan2 {
		t.Errorf("second later: %q", got)
	}
	if got := LaterISO(jan1, ""); got != jan1 {
		t.Errorf("empty second: %q", got)
	}
	// Equal times keep the first (different text, same instant).
	if got := LaterISO("2026-01-01T00:00:00Z", "2026-01-01T00:00:00.000Z"); got != "2026-01-01T00:00:00Z" {
		t.Errorf("equal times: %q", got)
	}
}

func TestParseISOAndMsOf(t *testing.T) {
	if ms, ok := ParseISO("2026-01-01T00:00:00.123Z"); !ok || ms != 1767225600123 {
		t.Errorf("ParseISO valid = %d, %v", ms, ok)
	}
	for _, bad := range []string{"", "yesterday", "2026-01-01"} {
		if ms, ok := ParseISO(bad); ok || ms != 0 {
			t.Errorf("ParseISO(%q) = %d, %v", bad, ms, ok)
		}
		if MsOf(bad) != 0 {
			t.Errorf("MsOf(%q) != 0", bad)
		}
	}
}

func rowAgo(id, status string, parent *string, agoMs int64) Row {
	return Row{ID: id, ParentID: parent, Status: status, LastEventMs: now - agoMs}
}

func TestDefaultConstantsMatchDefaults(t *testing.T) {
	want := Options{
		LingerMs: DefaultLingerMs, StaleMs: DefaultStaleMs, FinishedFadeMs: DefaultFinishedFadeMs,
		CallTimeoutMs: DefaultCallTimeoutMs, FileTTLMs: DefaultFileTTLMs,
	}
	if got := Defaults(); got != want {
		t.Fatalf("Defaults() = %+v, want the constants %+v", got, want)
	}
}

// A zero or negative field takes its default. A positive field stays.
func TestWithDefaultsTakesZeroAndNegative(t *testing.T) {
	if got := (Options{LingerMs: -5}).WithDefaults().LingerMs; got != DefaultLingerMs {
		t.Fatalf("negative linger = %d, want %d", got, DefaultLingerMs)
	}
	fields := []struct {
		name string
		set  func(*Options, int64)
		get  func(Options) int64
		def  int64
	}{
		{"LingerMs", func(o *Options, v int64) { o.LingerMs = v }, func(o Options) int64 { return o.LingerMs }, DefaultLingerMs},
		{"StaleMs", func(o *Options, v int64) { o.StaleMs = v }, func(o Options) int64 { return o.StaleMs }, DefaultStaleMs},
		{"FinishedFadeMs", func(o *Options, v int64) { o.FinishedFadeMs = v }, func(o Options) int64 { return o.FinishedFadeMs }, DefaultFinishedFadeMs},
		{"CallTimeoutMs", func(o *Options, v int64) { o.CallTimeoutMs = v }, func(o Options) int64 { return o.CallTimeoutMs }, DefaultCallTimeoutMs},
		{"FileTTLMs", func(o *Options, v int64) { o.FileTTLMs = v }, func(o Options) int64 { return o.FileTTLMs }, DefaultFileTTLMs},
	}
	for _, f := range fields {
		for _, v := range []int64{0, -1, -600_000} {
			var o Options
			f.set(&o, v)
			if got := f.get(o.WithDefaults()); got != f.def {
				t.Errorf("%s = %d: got %d, want default %d", f.name, v, got, f.def)
			}
		}
		var o Options
		f.set(&o, 7)
		if got := f.get(o.WithDefaults()); got != 7 {
			t.Errorf("%s = 7: got %d, want 7", f.name, got)
		}
	}
}

// An agent whose last event is exactly one linger old is absent.
func TestPresentIDsBoundaryIsExclusive(t *testing.T) {
	for _, o := range []Options{{}, {LingerMs: 2_000}} {
		linger := o.WithDefaults().LingerMs
		rows := []Row{
			rowAgo("edge", "idle", nil, linger),
			rowAgo("inside", "idle", nil, linger-1),
		}
		if got, want := sortedIDs(PresentIDs(rows, now, o)), []string{"inside"}; !reflect.DeepEqual(got, want) {
			t.Errorf("linger %d: present = %v, want %v", linger, got, want)
		}
	}
}

// A finished agent leaves after the fade window, not the linger window. The fade
// window does not apply to other statuses.
func TestPresentIDsFinishedUsesTheFadeWindow(t *testing.T) {
	for _, o := range []Options{{}, {FinishedFadeMs: 5_000}} {
		fade := o.WithDefaults().FinishedFadeMs
		rows := []Row{
			rowAgo("fin-edge", "finished", nil, fade),
			rowAgo("fin-inside", "finished", nil, fade-1),
			rowAgo("idle-at-fade", "idle", nil, fade),
			rowAgo("working-at-fade", "working", nil, fade),
		}
		want := []string{"fin-inside", "idle-at-fade", "working-at-fade"}
		if got := sortedIDs(PresentIDs(rows, now, o)); !reflect.DeepEqual(got, want) {
			t.Errorf("fade %d: present = %v, want %v", fade, got, want)
		}
	}
}

// Every ancestor of a present agent is present, however old. A descendant of a
// present agent is not made present by it.
func TestPresentIDsParentOfPresentIsPresent(t *testing.T) {
	rows := []Row{
		rowAgo("root", "idle", nil, 3_600_000),
		rowAgo("mid", "idle", str("root"), 3_000_000),
		rowAgo("leaf", "working", str("mid"), 1_000),
		rowAgo("old-child", "idle", str("leaf"), 3_000_000),
		rowAgo("other-root", "idle", nil, 3_600_000),
	}
	want := []string{"leaf", "mid", "root"}
	if got := sortedIDs(PresentIDs(rows, now, Options{})); !reflect.DeepEqual(got, want) {
		t.Fatalf("present = %v, want %v", got, want)
	}
}

// A parent that is not in the rows ends the walk without a panic.
func TestPresentIDsMissingParentIsNotAnError(t *testing.T) {
	rows := []Row{
		rowAgo("orphan", "idle", str("ghost"), 1_000),
		rowAgo("old-orphan", "idle", str("ghost"), 3_600_000),
	}
	if got, want := sortedIDs(PresentIDs(rows, now, Options{})), []string{"orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("present = %v, want %v", got, want)
	}
}

// A parent cycle and a self parent end the walk.
func TestPresentIDsCycleEnds(t *testing.T) {
	rows := []Row{
		rowAgo("a", "idle", str("b"), 1_000),
		rowAgo("b", "idle", str("a"), 9_000_000),
		rowAgo("self", "idle", str("self"), 1_000),
		rowAgo("old-self", "idle", str("old-self"), 9_000_000),
	}
	want := []string{"a", "b", "self"}
	if got := sortedIDs(PresentIDs(rows, now, Options{})); !reflect.DeepEqual(got, want) {
		t.Fatalf("present = %v, want %v", got, want)
	}
}

// An empty input has no present agents.
func TestPresentIDsEmpty(t *testing.T) {
	if got := PresentIDs(nil, now, Options{}); len(got) != 0 {
		t.Fatalf("present = %v, want none", got)
	}
}

// IsStale is true only for a working agent idle for more than the stale time.
func TestIsStaleBoundaryIsStrict(t *testing.T) {
	for _, o := range []Options{{}, {StaleMs: 10_000}} {
		stale := o.WithDefaults().StaleMs
		if IsStale("working", now-stale, now, o) {
			t.Errorf("stale %d: working at exactly the stale time is stale", stale)
		}
		if !IsStale("working", now-stale-1, now, o) {
			t.Errorf("stale %d: working one past the stale time is not stale", stale)
		}
		if IsStale("working", now-stale+1, now, o) {
			t.Errorf("stale %d: working one inside the stale time is stale", stale)
		}
		for _, status := range []string{"idle", "finished", "waiting", ""} {
			if IsStale(status, now-100*stale, now, o) {
				t.Errorf("stale %d: status %q is stale", stale, status)
			}
		}
	}
}

// The wire.Agent adapter gives the same result as PresentIDs over the same agents as rows.
func TestAdapterMatchesRows(t *testing.T) {
	agents := []wire.Agent{
		agent("fresh", func(a *wire.Agent) { a.LastEventAt = ago(599_999) }),
		agent("old", func(a *wire.Agent) { a.LastEventAt = ago(600_000) }),
		agent("root-old", func(a *wire.Agent) { a.LastEventAt = ago(3_600_000) }),
		agent("leaf", func(a *wire.Agent) { a.ParentID = str("root-old"); a.LastEventAt = ago(1_000) }),
		agent("fin-new", func(a *wire.Agent) { a.Status = "finished"; a.LastEventAt = ago(59_999) }),
		agent("fin-old", func(a *wire.Agent) { a.Status = "finished"; a.LastEventAt = ago(60_000) }),
		agent("orphan", func(a *wire.Agent) { a.ParentID = str("missing"); a.LastEventAt = ago(1_000) }),
		agent("cyc-a", func(a *wire.Agent) { a.ParentID = str("cyc-b"); a.LastEventAt = ago(1_000) }),
		agent("cyc-b", func(a *wire.Agent) { a.ParentID = str("cyc-a"); a.LastEventAt = ago(9_000_000) }),
		agent("no-time", func(a *wire.Agent) { a.LastEventAt = "" }),
	}
	rows := make([]Row, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, Row{ID: a.ID, ParentID: a.ParentID, Status: a.Status, LastEventMs: MsOf(a.LastEventAt)})
	}
	for _, o := range []Options{{}, {LingerMs: 2_000, FinishedFadeMs: 500}} {
		got, want := PresentAgentIDs(agents, now, o), PresentIDs(rows, now, o)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("options %+v: adapter = %v, rows = %v", o, sortedIDs(got), sortedIDs(want))
		}
		if len(got) == 0 {
			t.Errorf("options %+v: no agent is present, the comparison proves nothing", o)
		}
	}
}
