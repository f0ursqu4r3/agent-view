package cards

import (
	"strings"

	"agent-view/internal/cells"
	"agent-view/internal/store"
)

// StateWord is the state shown on an agent card.
type StateWord struct {
	Text  string
	Style cells.Style
	Flash bool // true only for STOPPED
}

// State returns the state word of an agent, by the table in the spec section
// "Agent card"; the first matching row wins. open holds the agent's open
// calls, newest first; the first is the shown call. A call or an agent with
// no event for more than staleMs is stale.
func State(a store.ViewAgent, open []*store.ViewCall, hasPresentChild bool, now, staleMs int64) StateWord {
	dim := cells.Style{Role: cells.RDim}
	cyan := cells.Style{Role: cells.RAgent}
	fresh := now-a.LastEventMs <= staleMs
	switch {
	case a.Status == "interrupted":
		return StateWord{"! STOPPED", cells.Style{Role: cells.RFail}, true}
	case a.Status == "finished":
		return StateWord{"✓ DONE", dim, false}
	case a.Status == "idle":
		return StateWord{"- IDLE", dim, false}
	}
	for _, c := range open {
		if need := humanRequest(c); need != "" {
			return StateWord{"! " + need, cells.Style{Role: cells.RSearch, Bold: true}, false}
		}
	}
	switch {
	case Waiting(a, open, hasPresentChild):
		return StateWord{"• WAITING", cyan, false}
	case len(open) > 0 && fresh:
		return StateWord{"• WORKING", cells.Style{Role: cells.ROK, Bold: true}, false}
	case len(open) > 0:
		return StateWord{"◌ STALE", dim, false}
	case a.Status == "working" && fresh:
		return StateWord{"• THINKING", cyan, false}
	case a.Status == "working":
		return StateWord{"◌ STALE", dim, false}
	}
	return StateWord{"- IDLE", dim, false}
}

// Only an explicit, open request warrants a human-attention indicator.
func humanRequest(c *store.ViewCall) string {
	if c.Ended {
		return ""
	}
	switch strings.TrimPrefix(c.RawName, "functions.") {
	case "AskUserQuestion", "request_user_input":
		return "INPUT"
	case "ExitPlanMode", "request_permissions":
		return "APPROVAL"
	}
	return ""
}

// Waiting reports whether the state word of an agent is WAITING: the shown
// open call (open[0]) is an agent call, a sub-agent of the agent is present,
// and no status row (STOPPED, DONE, IDLE) comes first. A waiting agent is
// not stale, and neither is its call card.
func Waiting(a store.ViewAgent, open []*store.ViewCall, hasPresentChild bool) bool {
	switch a.Status {
	case "interrupted", "finished", "idle":
		return false
	}
	return len(open) > 0 && open[0].Category == "agent" && hasPresentChild
}
