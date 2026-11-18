package store

import (
	"time"

	"agent-view/internal/schema"
)

// Lifecycle defaults from src/shared/lifecycle.ts.
const (
	DefaultLingerMs       int64 = 600_000   // 10 min
	DefaultStaleMs        int64 = 120_000   // 2 min
	DefaultFinishedFadeMs int64 = 60_000    // 1 min
	DefaultFileTTLMs      int64 = 1_800_000 // 30 min
)

type lifecycle struct {
	lingerMs, staleMs, finishedFadeMs, fileTTLMs int64
}

// resolve fills each zero or negative duration with its default.
func resolve(o Options) lifecycle {
	or := func(v, d int64) int64 {
		if v > 0 {
			return v
		}
		return d
	}
	return lifecycle{
		lingerMs:       or(o.LingerMs, DefaultLingerMs),
		staleMs:        or(o.StaleMs, DefaultStaleMs),
		finishedFadeMs: or(o.FinishedFadeMs, DefaultFinishedFadeMs),
		fileTTLMs:      or(o.FileTTLMs, DefaultFileTTLMs),
	}
}

// parseISO returns the Unix milliseconds of an RFC 3339 time.
func parseISO(iso string) (int64, bool) {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// msOf returns the Unix milliseconds of an ISO time. An empty or invalid time counts as the epoch.
func msOf(iso string) int64 {
	ms, _ := parseISO(iso)
	return ms
}

// isoOf formats Unix milliseconds like JavaScript's Date.prototype.toISOString.
func isoOf(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// presentAgentIDs returns the present agents: those with a recent event, plus every ancestor of a present agent.
func presentAgentIDs(agents []schema.Agent, now int64, o lifecycle) map[string]bool {
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
		if now-msOf(agents[i].LastEventAt) >= window {
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
func isStale(a schema.Agent, now int64, o lifecycle) bool {
	return a.Status == "working" && now-msOf(a.LastEventAt) > o.staleMs
}
