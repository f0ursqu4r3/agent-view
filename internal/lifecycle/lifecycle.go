// Package lifecycle holds the time and presence rules that the collector and
// the TUI store share. It ports the former TypeScript shared/lifecycle.ts and
// the laterIso and msOf helpers of collector/state.ts (removed in P2c; see git
// history).
package lifecycle

import (
	"time"

	"agent-view/internal/wire"
)

// Options are the lifecycle durations in milliseconds. A zero field takes its
// default in WithDefaults.
type Options struct {
	LingerMs       int64
	StaleMs        int64
	FinishedFadeMs int64
	CallTimeoutMs  int64
	FileTTLMs      int64
}

// Defaults returns the lifecycle values of the former TypeScript
// shared/lifecycle.ts (removed in P2c; see git history).
func Defaults() Options {
	return Options{
		LingerMs:       600_000,
		StaleMs:        120_000,
		FinishedFadeMs: 60_000,
		CallTimeoutMs:  600_000,
		FileTTLMs:      1_800_000,
	}
}

// WithDefaults returns o with each zero field set to its default.
func (o Options) WithDefaults() Options {
	d := Defaults()
	if o.LingerMs == 0 {
		o.LingerMs = d.LingerMs
	}
	if o.StaleMs == 0 {
		o.StaleMs = d.StaleMs
	}
	if o.FinishedFadeMs == 0 {
		o.FinishedFadeMs = d.FinishedFadeMs
	}
	if o.CallTimeoutMs == 0 {
		o.CallTimeoutMs = d.CallTimeoutMs
	}
	if o.FileTTLMs == 0 {
		o.FileTTLMs = d.FileTTLMs
	}
	return o
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

// PresentAgentIDs returns the present agents: those with a recent event, plus
// every ancestor of a present agent.
func PresentAgentIDs(agents []wire.Agent, now int64, o Options) map[string]bool {
	o = o.WithDefaults()
	byID := make(map[string]*wire.Agent, len(agents))
	for i := range agents {
		byID[agents[i].ID] = &agents[i]
	}
	present := map[string]bool{}
	for i := range agents {
		window := o.LingerMs
		if agents[i].Status == "finished" {
			window = o.FinishedFadeMs
		}
		if now-MsOf(agents[i].LastEventAt) >= window {
			continue
		}
		seen := map[string]bool{}
		for cur := &agents[i]; cur != nil && !seen[cur.ID]; {
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

// IsStale reports whether a working agent has had no event for longer than the
// stale time.
func IsStale(a wire.Agent, now int64, o Options) bool {
	return a.Status == "working" && now-MsOf(a.LastEventAt) > o.WithDefaults().StaleMs
}
