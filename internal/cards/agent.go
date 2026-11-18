package cards

import (
	"fmt"

	"agent-view/internal/cells"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// Agent card size and layout, in cells.
const (
	AgentW = 24
	AgentH = 7

	spriteX  = 2 // border, one space
	textX    = 12
	textW    = 10
	titleMax = AgentW - 2 - 4
)

// AgentIn is the input of AgentCard.
type AgentIn struct {
	Agent                      store.ViewAgent
	State                      StateWord
	Counters                   store.Counters
	Now                        int64
	HasSubs, Selected, Focused bool
}

// providerRole returns the color role of a provider.
func providerRole(provider string) cells.Role {
	switch provider {
	case "claude":
		return cells.RClaude
	case "codex":
		return cells.RCodex
	}
	return cells.RText
}

// seenAge formats a time since first sight: 42s, 31m, 2.5h.
func seenAge(ms int64) string {
	switch {
	case ms < 60_000:
		return fmt.Sprintf("%ds", max(ms, 0)/1000)
	case ms < 3_600_000:
		return fmt.Sprintf("%dm", ms/60_000)
	}
	return fmt.Sprintf("%.1fh", float64(ms)/3_600_000)
}

// AgentCard draws an agent card, 24 × 7. The border is heavy for a main
// agent and rounded for a sub-agent.
func AgentCard(in AgentIn) *cells.Grid {
	g := cells.New(AgentW, AgentH)
	a := in.Agent
	prov := providerRole(a.Provider)
	kind := cells.Heavy
	if a.ParentID != nil {
		kind = cells.Round
	}
	border := cells.Style{Role: cells.RDim}
	switch {
	case in.Selected || in.Focused:
		border = cells.Style{Role: cells.RBright}
	case a.Status == "working":
		border = cells.Style{Role: prov}
	}
	g.Box(0, 0, AgentW, AgentH, kind, border)
	if in.HasSubs {
		tee := "┬"
		if kind == cells.Heavy {
			tee = "┳"
		}
		g.Text(2, AgentH-1, 1, tee, border)
	}

	if name := text.Clean(a.Name); name != "" {
		n := g.Text(3, 0, titleMax, name, cells.Style{Role: prov, Bold: true})
		g.Text(2, 0, 1, " ", border)
		g.Text(3+n, 0, 1, " ", border)
	}

	dim := cells.Style{Role: cells.RDim}
	bright := cells.Style{Role: cells.RBright}
	for i, row := range Sprite(a.ID) {
		g.Text(spriteX, 1+i, 8, row, cells.Style{Role: prov})
	}

	provider := text.Clean(a.Provider)
	if a.ParentID != nil {
		provider += " sub"
	}
	g.Text(textX, 1, textW, provider, dim)
	g.Text(textX, 2, textW, in.State.Text, in.State.Style)
	pair := func(y int, label, value string, vs cells.Style) {
		n := g.Text(textX, y, textW, label, dim)
		g.Text(textX+n, y, textW-n, value, vs)
	}
	pair(3, "calls ", fmt.Sprint(in.Counters.Calls), bright)
	faultStyle := bright
	if in.Counters.Faults > 0 {
		faultStyle = cells.Style{Role: cells.RFail}
	}
	pair(4, "fault ", fmt.Sprint(in.Counters.Faults), faultStyle)
	pair(5, "seen ", seenAge(in.Now-in.Counters.FirstSeenMs), bright)
	return g
}
