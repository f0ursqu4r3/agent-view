// Package tui joins the board, the event log, the status bar, and the key bar
// into one cell grid for the terminal.
package tui

import (
	"fmt"

	"github.com/rivo/uniseg"

	"agent-view/internal/anim"
	"agent-view/internal/board"
	"agent-view/internal/cells"
	"agent-view/internal/store"
)

// Screen limits and layout, in cells and lines.
const (
	MinW = 80 // narrower terminals get the "too small" screen
	MinH = 24 // shorter terminals get the "too small" screen

	marginX  = 1            // empty column left and right of the board, log, and bars
	boardTop = 2            // the status bar, then one blank line
	logLines = 8            // event lines of the default log
	logH     = logLines + 3 // plus the prompt line and the border
)

// HostStatus is the status bar segment of one host. State is up, connecting,
// or down; Degraded counts the sources that are not ok.
type HostStatus struct {
	Label, State string
	Degraded     int
}

// Status holds the values of the status bar.
type Status struct {
	Hosts                                []HostStatus
	Agents, Working, CallsPerMin, Faults int
	Paused                               bool
	Clock                                string
}

// Input is everything one frame shows. Anim and Log may be nil.
type Input struct {
	Store       *store.Store
	UI          board.UI
	Anim        *anim.Engine
	Log         *board.Log
	Status      Status
	Now         int64
	Spin        int
	W, H        int
	Diagnostics []string
}

// Hit is the screen rectangle of one agent card (one line for a collapsed
// row), for mouse clicks. It holds only the visible part of the card.
type Hit struct {
	AgentID    string
	X, Y, W, H int
}

// Compose draws one frame, W × H: the status bar, the board, the event log,
// and the key bar, with the open panel on top. Below 80 × 24 it draws only
// the "too small" screen. It calls Anim.Frame once for each agent row on the
// board. The hits are the visible agent cards; there are none while a panel
// is open.
func Compose(in Input) (*cells.Grid, []Hit) {
	g := cells.New(in.W, in.H)
	if g.W < MinW || g.H < MinH {
		tooSmall(g)
		return g, nil
	}
	paused := in.UI.Paused || in.Status.Paused
	drawStatus(g, in.Status, paused)

	lh := logH
	if in.UI.TallLog {
		lh = g.H / 2
	}
	bh := g.H - boardTop - lh - 1
	b, hits := drawBoard(in, g.W, bh)
	g.Blit(b, 0, boardTop)
	for i := range hits {
		hits[i].Y += boardTop
	}

	log := in.Log
	if log == nil {
		log = &board.Log{}
	}
	g.Blit(log.Render(g.W-2*marginX, lh, in.UI.Focus), marginX, g.H-1-lh)
	drawKeys(g, g.H-1, paused)

	if p := panel(in, g.W, g.H); p != nil {
		g.Blit(p, (g.W-p.W)/2, (g.H-p.H)/2)
		hits = nil
	}
	return g, hits
}

// tooSmall draws the screen for a terminal below the minimum size.
func tooSmall(g *cells.Grid) {
	lines := []string{
		"terminal too small",
		fmt.Sprintf("need %d × %d, have %d × %d", MinW, MinH, g.W, g.H),
	}
	y := (g.H - len(lines)) / 2
	for i, s := range lines {
		w := min(width(s), g.W)
		g.Text((g.W-w)/2, y+i, g.W, s, cells.Style{Role: cells.RDim})
	}
}

// panel returns the open panel, sized for a w × h screen, or nil.
func panel(in Input, w, h int) *cells.Grid {
	switch in.UI.Panel {
	case board.PanelCoverage:
		return CoveragePanel(coverage(in.Store), in.Diagnostics, min(w-4, 100), h-4)
	case board.PanelHelp:
		return HelpPanel(min(w-4, helpW), min(h-2, len(helpKeys)+2))
	}
	return nil
}

// drawBoard draws the agent rows into a w × h grid, scrolled by UI.Top, and
// returns the hits in board coordinates. Every row gets its Frame call, the
// rows out of view included, because their animated heights place the rows
// below them.
func drawBoard(in Input, w, h int) (*cells.Grid, []Hit) {
	b := cells.New(w, h)
	var rows []board.Row
	if in.Store != nil {
		rows = board.Rows(in.Store, in.UI, in.Now)
	}
	if len(rows) == 0 {
		b.Text(marginX, 0, w-2*marginX, "waiting for agents…", cells.Style{Role: cells.RDim})
		return b, nil
	}
	eng := in.Anim
	if eng == nil {
		eng = anim.New(true) // reduced: done values only
	}
	plans := planRows(in, rows, w, eng)
	total := 0
	for _, p := range plans {
		total += p.f.RowH
	}
	top := min(max(in.UI.Top, 0), max(0, total-h))

	var hits []Hit
	y := -top
	for _, p := range plans {
		if y < h && y+p.f.RowH > 0 {
			rg := drawRow(in, p, w)
			if in.UI.Focus != "" && p.row.AgentID != in.UI.Focus {
				rg.Dim()
			}
			b.Blit(rg, 0, y)
			if hit, ok := p.hit(y, h); ok {
				hits = append(hits, hit)
			}
		}
		y += p.f.RowH
	}
	return b, hits
}

// width returns the layout width of s, as Grid.Text counts it.
func width(s string) int {
	n := 0
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		n += min(max(gr.Width(), 0), 2)
	}
	return n
}

// prefix returns the longest prefix of s that is at most n cells wide.
func prefix(s string, n int) string {
	used, end := 0, 0
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		gw := min(max(gr.Width(), 0), 2)
		if used+gw > n {
			break
		}
		used += gw
		_, end = gr.Positions()
	}
	return s[:end]
}

// writer puts styled pieces left to right on one line, up to end. A piece
// that does not fit is cut with an ellipsis, and later pieces are dropped; a
// piece dropped whole turns the last cell into an ellipsis.
type writer struct {
	g         *cells.Grid
	x, y, end int
}

func (wr *writer) put(s string, st cells.Style) {
	if s == "" {
		return
	}
	if wr.x >= wr.end {
		if wr.end > 0 && wr.g.C[wr.y*wr.g.W+wr.end-1].G != "…" {
			wr.g.Text(wr.end-1, wr.y, 1, "…", cells.Style{Role: cells.RDim})
		}
		return
	}
	wr.x += wr.g.Text(wr.x, wr.y, wr.end-wr.x, s, st)
}
