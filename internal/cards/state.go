package cards

import (
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
	case len(open) > 0 && open[0].Category == "agent" && hasPresentChild:
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
