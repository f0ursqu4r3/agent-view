// Package lifecycle holds the time and presence rules that the collector and
// the TUI store share. It ports the former TypeScript shared/lifecycle.ts and
// the laterIso and msOf helpers of collector/state.ts (removed in P2c; see git
// history).
package lifecycle

import (
	"time"

	"agent-view/internal/wire"
)

// The lifecycle durations in milliseconds, from the former TypeScript
// shared/lifecycle.ts (removed in P2c; see git history).
const (
	DefaultLingerMs       int64 = 600_000   // 10 min
	DefaultStaleMs        int64 = 120_000   // 2 min
	DefaultFinishedFadeMs int64 = 60_000    // 1 min
	DefaultCallTimeoutMs  int64 = 600_000   // 10 min
	DefaultFileTTLMs      int64 = 1_800_000 // 30 min
)

// Options are the lifecycle durations in milliseconds. A zero or negative
// field takes its default in WithDefaults.
type Options struct {
	LingerMs       int64
	StaleMs        int64
	FinishedFadeMs int64
	CallTimeoutMs  int64
	FileTTLMs      int64
}

// Defaults returns the default lifecycle durations.
func Defaults() Options {
	return Options{
		LingerMs:       DefaultLingerMs,
		StaleMs:        DefaultStaleMs,
		FinishedFadeMs: DefaultFinishedFadeMs,
		CallTimeoutMs:  DefaultCallTimeoutMs,
		FileTTLMs:      DefaultFileTTLMs,
	}
}

// WithDefaults returns o with each zero or negative field set to its default.
func (o Options) WithDefaults() Options {
	or := func(v, d int64) int64 {
		if v > 0 {
			return v
		}
		return d
	}
	return Options{
		LingerMs:       or(o.LingerMs, DefaultLingerMs),
		StaleMs:        or(o.StaleMs, DefaultStaleMs),
		FinishedFadeMs: or(o.FinishedFadeMs, DefaultFinishedFadeMs),
		CallTimeoutMs:  or(o.CallTimeoutMs, DefaultCallTimeoutMs),
		FileTTLMs:      or(o.FileTTLMs, DefaultFileTTLMs),
	}
}

// ParseISO returns the Unix milliseconds of an RFC 3339 time. It reports false
// for any other text.
func ParseISO(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// MsOf returns the Unix milliseconds of an ISO time. An empty or invalid time
// counts as the epoch.
func MsOf(s string) int64 {
	ms, _ := ParseISO(s)
	return ms
}

// LaterISO returns b only when its time is greater than the time of a. An
// empty time loses to any other time.
func LaterISO(a, b string) string {
	if MsOf(b) > MsOf(a) {
		return b
	}
	return a
}

// Row is what the presence rules need of one agent. The collector and the
// viewer store both build rows, so they share one set of rules.
type Row struct {
	ID          string
	ParentID    *string
	Status      string
	LastEventMs int64
}

// PresentIDs returns the present agents: those with a recent event, plus every
// ancestor of a present agent. A finished agent counts as recent for the fade
// time, any other agent for the linger time. An event exactly one window old is
// not recent.
func PresentIDs(rows []Row, now int64, o Options) map[string]bool {
	o = o.WithDefaults()
	byID := make(map[string]*Row, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	present := map[string]bool{}
	for i := range rows {
		window := o.LingerMs
		if rows[i].Status == "finished" {
			window = o.FinishedFadeMs
		}
		if now-rows[i].LastEventMs >= window {
			continue
		}
		seen := map[string]bool{}
		for cur := &rows[i]; cur != nil && !seen[cur.ID]; {
			seen[cur.ID] = true
			present[cur.ID] = true
			if cur.ParentID == nil {
				cur = nil
			} else {
				cur = byID[*cur.ParentID]
			}
		}
	}
	return present
}

// PresentAgentIDs is PresentIDs over wire agents. An empty or invalid
// LastEventAt counts as the epoch.
func PresentAgentIDs(agents []wire.Agent, now int64, o Options) map[string]bool {
	rows := make([]Row, len(agents))
	for i := range agents {
		a := &agents[i]
		rows[i] = Row{ID: a.ID, ParentID: a.ParentID, Status: a.Status, LastEventMs: MsOf(a.LastEventAt)}
	}
	return PresentIDs(rows, now, o)
}

// IsStale reports whether an agent is working and has had no event for longer
// than the stale time.
func IsStale(status string, lastEventMs, now int64, o Options) bool {
	return status == "working" && now-lastEventMs > o.WithDefaults().StaleMs
}
