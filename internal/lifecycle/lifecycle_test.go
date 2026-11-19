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
		if got := IsStale(c.a, now, c.o); got != c.want {
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
