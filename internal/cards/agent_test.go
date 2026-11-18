package cards

import (
	"strings"
	"testing"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

func TestSpriteStable(t *testing.T) {
	if Sprite("x") != Sprite("x") {
		t.Fatal("Sprite is not stable")
	}
	if Sprite("x") == Sprite("y") {
		t.Fatal("Sprite(x) equals Sprite(y)")
	}
	for _, id := range []string{"x", "y", "agent-view", "日本", ""} {
		for i, row := range Sprite(id) {
			if n := len([]rune(row)); n != 8 {
				t.Fatalf("%q row %d has %d cells", id, i, n)
			}
			if strings.Trim(row, " ▀▄█") != "" {
				t.Fatalf("%q row %d has other glyphs: %q", id, i, row)
			}
		}
	}
}

func TestSpriteMirrored(t *testing.T) {
	for _, row := range Sprite("agent-view") {
		r := []rune(row)
		for i := 0; i < 4; i++ {
			if r[i] != r[7-i] {
				t.Fatalf("row %q is not mirrored", row)
			}
		}
	}
}

// The expected rows come from running the prototype's JavaScript (hashS,
// mulberry, sprite in term-cards.html). "日本" checks UTF-16 code units.
func TestSpriteMatchesPrototype(t *testing.T) {
	tests := map[string][4]string{
		"agent-view": {" ▄▀██▀▄ ", "▄█▀▀▀▀█▄", "▀▄▀  ▀▄▀", "▄ ████ ▄"},
		"x":          {"▄▄ ▀▀ ▄▄", "█▄▀▀▀▀▄█", " █▀▄▄▀█ ", "▀██████▀"},
		"日本":         {"▄▄█▀▀█▄▄", "██▀  ▀██", " ▄▀▄▄▀▄ ", "▀██▄▄██▀"},
		"machina-ui": {"▄ ████ ▄", "▀▄▀██▀▄▀", "███▀▀███", "██ ██ ██"},
	}
	for id, want := range tests {
		if got := Sprite(id); got != want {
			t.Errorf("Sprite(%q) = %q, want %q", id, got, want)
		}
	}
}

func viewAgent(id, name, provider, status string, parent *string) store.ViewAgent {
	return store.ViewAgent{
		Agent:       schema.Agent{ID: id, Name: name, Provider: provider, Status: status, ParentID: parent},
		LastEventMs: 0,
	}
}

func TestAgentCardGolden(t *testing.T) {
	const now = int64(3_600_000 * 3)
	parent := "p"
	working := StateWord{"• WORKING", cells.Style{Role: cells.ROK, Bold: true}, false}
	idle := StateWord{"- IDLE", cells.Style{Role: cells.RDim}, false}
	counters := store.Counters{Calls: 212, Faults: 3, FirstSeenMs: now - 9_000_000}
	tests := []struct {
		name string
		in   AgentIn
	}{
		{"main_working", AgentIn{Agent: viewAgent("av", "agent-view", "claude", "working", nil), State: working, Counters: counters, Now: now}},
		{"sub_idle", AgentIn{Agent: viewAgent("vr", "Verify review", "codex", "idle", &parent), State: idle, Counters: store.Counters{Calls: 4, FirstSeenMs: now - 31*60_000}, Now: now}},
		{"has_subs", AgentIn{Agent: viewAgent("mu", "machina-ui", "claude", "working", nil), State: working, Counters: store.Counters{Calls: 9, FirstSeenMs: now - 42_000}, Now: now, HasSubs: true}},
		{"sub_has_subs", AgentIn{Agent: viewAgent("dl", "Data-load", "claude", "working", &parent), State: working, Counters: counters, Now: now, HasSubs: true}},
		{"selected", AgentIn{Agent: viewAgent("eo", "eos-api", "codex", "idle", nil), State: idle, Counters: counters, Now: now, Selected: true}},
		{"focused_stopped", AgentIn{Agent: viewAgent("eo", "eos-api", "codex", "interrupted", nil), State: StateWord{"! STOPPED", cells.Style{Role: cells.RFail}, true}, Counters: counters, Now: now, Focused: true}},
		{"long_name", AgentIn{Agent: viewAgent("dl", "Data-load review of the API", "claude", "working", &parent), State: working, Counters: counters, Now: now}},
		{"cleaned", AgentIn{Agent: viewAgent("x", "bad\x1b[31m\nname", "cl\x07aude", "working", nil), State: working, Counters: counters, Now: now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := AgentCard(tt.in)
			if g.W != 24 || g.H != 7 {
				t.Fatalf("size = %dx%d, want 24x7", g.W, g.H)
			}
			checkGolden(t, "agent_"+tt.name, g)
		})
	}
}

func TestSeenAge(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0s"}, {42_000, "42s"}, {59_999, "59s"}, {60_000, "1m"}, {31 * 60_000, "31m"},
		{3_599_000, "59m"}, {3_600_000, "1.0h"}, {9_000_000, "2.5h"}, {-5, "0s"},
	}
	for _, tt := range tests {
		if got := seenAge(tt.ms); got != tt.want {
			t.Errorf("seenAge(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}
