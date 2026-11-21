package cards

import (
	"testing"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

const (
	nowMs   = int64(10_000_000)
	staleMs = int64(120_000)
)

func agentWith(status string, lastEventMs int64) store.ViewAgent {
	return store.ViewAgent{
		Agent:       schema.Agent{ID: "a", Provider: "claude", Name: "alpha", Status: status},
		LastEventMs: lastEventMs,
	}
}

func openCall(category string) []*store.ViewCall {
	return []*store.ViewCall{{Call: schema.Call{AgentID: "a", CallID: "c", Category: category}}}
}

func TestStateOrder(t *testing.T) {
	fresh := nowMs - 5_000
	old := nowMs - 5*60_000
	dim := cells.Style{Role: cells.RDim}
	tests := []struct {
		name  string
		agent store.ViewAgent
		open  []*store.ViewCall
		child bool
		want  StateWord
	}{
		{"interrupted", agentWith("interrupted", fresh), nil, false, StateWord{"! STOPPED", cells.Style{Role: cells.RFail}, true}},
		{"interrupted with open call", agentWith("interrupted", fresh), openCall("read"), false, StateWord{"! STOPPED", cells.Style{Role: cells.RFail}, true}},
		{"finished", agentWith("finished", fresh), nil, false, StateWord{"✓ DONE", dim, false}},
		{"finished with open call", agentWith("finished", fresh), openCall("read"), false, StateWord{"✓ DONE", dim, false}},
		{"idle", agentWith("idle", fresh), nil, false, StateWord{"- IDLE", dim, false}},
		{"waiting", agentWith("working", fresh), openCall("agent"), true, StateWord{"• WAITING", cells.Style{Role: cells.RAgent}, false}},
		{"waiting with no event for 5 min", agentWith("working", old), openCall("agent"), true, StateWord{"• WAITING", cells.Style{Role: cells.RAgent}, false}},
		{"agent call without present child", agentWith("working", fresh), openCall("agent"), false, StateWord{"• WORKING", cells.Style{Role: cells.ROK, Bold: true}, false}},
		{"child but call is not agent", agentWith("working", fresh), openCall("read"), true, StateWord{"• WORKING", cells.Style{Role: cells.ROK, Bold: true}, false}},
		{"working", agentWith("working", fresh), openCall("command"), false, StateWord{"• WORKING", cells.Style{Role: cells.ROK, Bold: true}, false}},
		{"open call, stale", agentWith("working", old), openCall("command"), false, StateWord{"◌ STALE", dim, false}},
		{"thinking", agentWith("working", fresh), nil, false, StateWord{"• THINKING", cells.Style{Role: cells.RAgent}, false}},
		{"working, no call, stale", agentWith("working", old), nil, false, StateWord{"◌ STALE", dim, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := State(tt.agent, tt.open, tt.child, nowMs, staleMs); got != tt.want {
				t.Fatalf("State = %+v, want %+v", got, tt.want)
			}
			if got, want := Waiting(tt.agent, tt.open, tt.child), tt.want.Text == "• WAITING"; got != want {
				t.Fatalf("Waiting = %v, want %v", got, want)
			}
		})
	}
}

func TestStateStaleBoundary(t *testing.T) {
	a := agentWith("working", nowMs-staleMs)
	if got := State(a, nil, false, nowMs, staleMs); got.Text != "• THINKING" {
		t.Fatalf("exactly 2 min: %q", got.Text)
	}
	a = agentWith("working", nowMs-staleMs-1)
	if got := State(a, nil, false, nowMs, staleMs); got.Text != "◌ STALE" {
		t.Fatalf("just past 2 min: %q", got.Text)
	}
}

func TestExplicitHumanRequests(t *testing.T) {
	for name, want := range map[string]string{
		"AskUserQuestion": "! INPUT", "functions.request_user_input": "! INPUT",
		"ExitPlanMode": "! APPROVAL", "functions.request_permissions": "! APPROVAL",
		"Bash": "• WORKING", "request_user_input_async": "• WORKING",
	} {
		calls := openCall("other")
		calls[0].RawName = name
		if got := State(agentWith("working", nowMs), calls, false, nowMs, staleMs); got.Text != want {
			t.Errorf("%s: got %q, want %q", name, got.Text, want)
		}
		calls[0].Ended = true
		if humanRequest(calls[0]) != "" {
			t.Errorf("%s still requests attention after completion", name)
		}
	}
}
