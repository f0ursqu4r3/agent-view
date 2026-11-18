package cards

import (
	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

// History card size, in cells.
const (
	HistoryW = 14
	HistoryH = 4
)

// resultWord returns the word and style for the result of an ended call:
// ok, fail, or ??.
func resultWord(c *store.ViewCall, upper bool) (string, cells.Style) {
	switch {
	case c.Ok == nil:
		return "??", cells.Style{Role: cells.RDim}
	case *c.Ok:
		return "ok", cells.Style{Role: cells.ROK}
	}
	if upper {
		return "FAIL", cells.Style{Role: cells.RFail}
	}
	return "fail", cells.Style{Role: cells.RFail}
}

// HistoryCard draws a history card, 14 × 4: the category label as title, the
// target, and the result with the duration. The border is dim.
func HistoryCard(c *store.ViewCall) *cells.Grid {
	g := cells.New(HistoryW, HistoryH)
	border := cells.Style{Role: cells.RDim}
	g.Box(0, 0, HistoryW, HistoryH, cells.Square, border)
	label := schema.Label(c.Category)
	g.Text(2, 0, 1, " ", border)
	n := g.Text(3, 0, HistoryW-6, label, cells.Style{Role: cells.RoleOf(c.Category)})
	g.Text(3+n, 0, 1, " ", border)

	putTarget(g, 1, 1, HistoryW-2, c)
	word, st := resultWord(c, false)
	n = g.Text(1, 2, HistoryW-2, word, st)
	if c.Ended {
		g.Text(1+n, 2, HistoryW-2-n, " "+Elapsed(c.EndMs-c.StartMs), cells.Style{Role: cells.RDim})
	}
	return g
}
