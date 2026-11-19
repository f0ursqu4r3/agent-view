package store

import (
	"time"

	"agent-view/internal/lifecycle"
	"agent-view/internal/schema"
)

// Lifecycle defaults from the former TypeScript shared/lifecycle.ts (removed in
// P2c; see git history).
const (
	DefaultLingerMs       int64 = 600_000   // 10 min
	DefaultStaleMs        int64 = 120_000   // 2 min
	DefaultFinishedFadeMs int64 = 60_000    // 1 min
	DefaultFileTTLMs      int64 = 1_800_000 // 30 min
)

type windows struct {
	lingerMs, staleMs, finishedFadeMs, fileTTLMs int64
}

// resolve fills each zero or negative duration with its default.
func resolve(o Options) windows {
	or := func(v, d int64) int64 {
		if v > 0 {
			return v
		}
		return d
	}
	return windows{
		lingerMs:       or(o.LingerMs, DefaultLingerMs),
		staleMs:        or(o.StaleMs, DefaultStaleMs),
		finishedFadeMs: or(o.FinishedFadeMs, DefaultFinishedFadeMs),
		fileTTLMs:      or(o.FileTTLMs, DefaultFileTTLMs),
	}
}

// isoOf formats Unix milliseconds like JavaScript's Date.prototype.toISOString.
func isoOf(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// presentAgentIDs returns the present agents: those with a recent event, plus every ancestor of a present agent.
func presentAgentIDs(agents []schema.Agent, now int64, o windows) map[string]bool {
	byID := make(map[string]*schema.Agent, len(agents))
	for i := range agents {
		byID[agents[i].ID] = &agents[i]
	}
	present := map[string]bool{}
	for i := range agents {
		window := o.lingerMs
		if agents[i].Status == "finished" {
			window = o.finishedFadeMs
		}
		if now-lifecycle.MsOf(agents[i].LastEventAt) >= window {
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

// isStale reports whether a working agent has had no event for longer than the stale time.
func isStale(a schema.Agent, now int64, o windows) bool {
	return a.Status == "working" && now-lifecycle.MsOf(a.LastEventAt) > o.staleMs
}
