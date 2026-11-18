package tui

import (
	"agent-view/internal/anim"
	"agent-view/internal/board"
	"agent-view/internal/cards"
	"agent-view/internal/cells"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// Row layout, in cells, from the board's left margin.
const (
	subIndent    = 5                       // sub-agent rows: the gutter, then the card
	callX        = cards.AgentW + 1        // the call card, one column right of the agent card
	histX        = callX + cards.CallW + 1 // the first history card
	histStep     = cards.HistoryW + 1      // history cards are one column apart
	focusOutput  = 10                      // output lines of the focused command card
	dimHistoryAt = 2                       // history cards from this index are dimmer
	gutterW      = 5                       // the gutter columns 0-4
)

// rowPlan holds what one agent row draws, decided before any drawing so the
// animated heights can place the rows.
type rowPlan struct {
	row     board.Row
	rows    []board.Row
	index   int
	w       int // the board width
	agent   store.ViewAgent
	open    []*store.ViewCall
	shown   *store.ViewCall // the call on the card, or nil
	prev    *store.ViewCall // the call shown before it, for the leaving card
	hist    []*store.ViewCall
	hasKids bool // a present agent has this agent as its parent
	callH   int  // the call card height
	target  int  // the row height the board wants
	f       anim.AgentFrame
}

// planRows looks up the calls of each row, picks its cards, and calls Frame
// once for each row.
func planRows(in Input, rows []board.Row, w int, eng *anim.Engine) []*rowPlan {
	s := in.Store
	plans := make([]*rowPlan, 0, len(rows))
	parents := map[string]bool{}
	for i, r := range rows {
		a, _ := s.Agent(r.AgentID) // board.Rows keeps only stored agents
		if a.ParentID != nil {
			parents[*a.ParentID] = true
		}
		plans = append(plans, &rowPlan{row: r, rows: rows, index: i, w: w, agent: a, callH: cards.CallH, target: r.H})
	}
	for _, p := range plans {
		id := p.row.AgentID
		p.hasKids = parents[id]
		p.open = s.OpenCalls(id)
		histN := 0
		if p.row.Full {
			histN = board.HistoryCount(w-2*marginX, p.row.Sub)
		}
		ended := s.RecentEnded(id, histN+2)
		// The shown call is the newest open call, else the last ended one;
		// the call before it is the next in that same order.
		cands := append(append([]*store.ViewCall(nil), p.open...), ended...)
		if len(cands) > 0 {
			p.shown = cands[0]
		}
		if len(cands) > 1 {
			p.prev = cands[1]
		}
		if len(p.open) == 0 && len(ended) > 0 {
			ended = ended[1:] // the shown call is not history
		}
		p.hist = ended[:min(histN, len(ended))]
		if p.row.Full && in.UI.Focus == id && p.shown != nil && p.shown.Category == "command" {
			p.callH = 5 + focusOutput
			p.target += p.callH - cards.CallH
		}
		p.f = eng.Frame(id, p.target, len(p.hist))
	}
	return plans
}

// root returns the agent's project root, else its cwd, else "".
func root(a store.ViewAgent) string {
	switch {
	case a.ProjectRoot != nil:
		return *a.ProjectRoot
	case a.Cwd != nil:
		return *a.Cwd
	}
	return ""
}

// head returns the number of host header lines of the row.
func (p *rowPlan) head() int {
	if p.row.HostHeader != "" {
		return 1
	}
	return 0
}

// cardX returns the column of the agent card in the row grid.
func (p *rowPlan) cardX() int {
	if p.row.Sub {
		return marginX + subIndent
	}
	return marginX
}

// gutterDrawing reports whether the gutter of a new sub-agent is still
// drawing; the rest of the row prints after it.
func (p *rowPlan) gutterDrawing() bool { return p.f.GutterRows < p.target }

// hit returns the visible part of the agent card, or of the collapsed line,
// for a row whose top is at line y of a board h lines high.
func (p *rowPlan) hit(y, h int) (Hit, bool) {
	if p.gutterDrawing() {
		return Hit{}, false
	}
	x := p.cardX()
	hit := Hit{AgentID: p.row.AgentID, X: x, Y: y + p.head(), W: cards.AgentW, H: cards.AgentH}
	if !p.row.Full {
		hit.W, hit.H = p.lineW(), 1
	}
	bottom := min(hit.Y+hit.H, y+p.f.RowH, h)
	hit.Y = max(hit.Y, 0)
	hit.H = bottom - hit.Y
	return hit, hit.H > 0
}

// lineW returns the width of a collapsed line.
func (p *rowPlan) lineW() int { return p.w - marginX - p.cardX() }

// Gutter verticals, as board.Gutter draws them in columns 0-4.
const (
	gutterLine   = "  │"
	gutterDashed = "  ┆"
)

// gutterBelow returns the gutter of the lines past the board's row height,
// in a row that is taller (a focused card, a row that collapses): the tree
// line goes on below a sub-agent that has later siblings, and below a full
// main row whose sub-agents follow.
func (p *rowPlan) gutterBelow() string {
	r := p.row
	switch {
	case r.Sub && !r.LastSib && r.SessionLink:
		return gutterDashed
	case r.Sub && !r.LastSib:
		return gutterLine
	case r.Sub || !r.Full || !p.hasSubRow():
		return ""
	case p.rows[p.index+1].SessionLink:
		return gutterDashed
	}
	return gutterLine
}

// hasSubRow reports whether a sub-agent row follows this main row, so its
// card gets a tee for the tree line (the same rule as board.Gutter).
func (p *rowPlan) hasSubRow() bool {
	return !p.row.Sub && p.index+1 < len(p.rows) && p.rows[p.index+1].Sub
}

// drawRow draws one row into a grid as wide as the board and as high as its
// animated height.
func drawRow(in Input, p *rowPlan, w int) *cells.Grid {
	g := cells.New(w, max(p.target, p.f.RowH))
	dim := cells.Style{Role: cells.RDim}
	head := p.head()
	if head > 0 {
		n := g.Text(marginX, 0, w-2*marginX, text.Clean(p.row.HostHeader), cells.Style{Role: cells.RBright, Bold: true})
		for x := marginX + n + 1; x < w-marginX; x++ {
			g.Text(x, 0, 1, "─", cells.Style{Role: cells.RFaint})
		}
	}

	gut := board.Gutter(p.rows, p.index)
	lines := g.H
	if p.gutterDrawing() {
		lines = p.f.GutterRows
	}
	below := p.gutterBelow()
	for l := 0; l < lines; l++ {
		s := below
		if l < len(gut) {
			s = gut[l]
		}
		g.Text(marginX, l, gutterW, s, dim)
	}
	if !p.gutterDrawing() {
		if p.row.Full {
			drawFull(in, p, g, head)
		} else {
			drawCollapsed(in, p, g, head)
		}
	}
	g.ClipRows(0, p.f.RowH)
	return g
}

// whereFrames is the length of the "where" type-out in frames; the engine
// reports 0 to whereFrames in AgentFrame.WhereChars.
const whereFrames = 6

// state returns the agent's state word. STOPPED flashes once: it is inverse
// on the inverse frames of the engine's fail flash, so the model starts that
// flash (Anim.OnFail) when an agent is interrupted.
func (p *rowPlan) state(in Input) cards.StateWord {
	st := cards.State(p.agent, p.open, p.hasKids, in.Now, store.DefaultStaleMs)
	if st.Flash && p.f.Flash {
		st.Style.Inverse = true
	}
	return st
}

// drawFull draws a full row from line head: the agent card, the call card
// over the leaving one, the history cards, and the "where" line.
func drawFull(in Input, p *rowPlan, g *cells.Grid, head int) {
	a := p.agent
	x := p.cardX()
	g.Blit(cards.AgentCard(cards.AgentIn{
		Agent: a, State: p.state(in), Counters: in.Store.Counters(a.ID), Now: in.Now,
		HasSubs: p.hasSubRow(), Selected: in.UI.Selected == a.ID, Focused: in.UI.Focus == a.ID,
	}), x, head)

	cx := x + callX
	if p.prev != nil && p.f.LeavingRows > 0 {
		lc := callCard(in, p, p.prev, 0, 0)
		lc.ClipRows(0, p.f.LeavingRows)
		g.Blit(lc, cx, head)
	}
	g.Blit(shownCard(in, p), cx, head)

	// Oldest first, so a newer card that moves right slides out from under
	// the next older one.
	hy := head + p.callH - cards.HistoryH
	for i := len(p.hist) - 1; i >= 0; i-- {
		hc := cards.HistoryCard(p.hist[i])
		if i >= dimHistoryAt {
			hc.Dim()
		}
		if i == 0 {
			hc.ClipRows(0, p.f.HistPrintRows)
		}
		off := 0
		if i < len(p.f.HistOffset) {
			off = p.f.HistOffset[i]
		}
		g.Blit(hc, x+histX+i*histStep+off, hy)
	}
	drawWhere(p, g, cx, head+p.callH)
}

// shownCard returns the call card as this frame prints it: the shown call,
// or an empty dim card for an agent without calls; inverse red on a flash
// frame; cut to the printed lines.
func shownCard(in Input, p *rowPlan) *cells.Grid {
	var card *cells.Grid
	if p.shown == nil {
		card = cells.New(cards.CallW, cards.CallH)
		card.Box(0, 0, cards.CallW, cards.CallH, cells.Square, cells.Style{Role: cells.RDim})
	} else {
		other, lines := 0, 0
		if !p.shown.Ended {
			other = len(p.open) - 1
		}
		if p.callH > cards.CallH {
			lines = focusOutput
		}
		card = callCard(in, p, p.shown, other, lines)
	}
	if p.f.Flash {
		for i := range card.C {
			card.C[i].S = cells.Style{Role: cells.RFail, Inverse: true}
		}
	}
	// The print runs over the first 7 lines; when it is done, a taller
	// (focused) card shows whole.
	if p.f.CallRows < cards.CallH {
		card.ClipRows(0, p.f.CallRows)
	}
	return card
}

// callCard draws a call of the row's agent. An open call is stale when the
// agent has had no event for the stale time.
func callCard(in Input, p *rowPlan, c *store.ViewCall, otherOpen, outputLines int) *cells.Grid {
	return cards.CallCard(cards.CallIn{
		Call: c, ProjectRoot: root(p.agent), OtherOpen: otherOpen, Now: in.Now, Spin: in.Spin,
		Stale:       !c.Ended && in.Now-p.agent.LastEventMs > store.DefaultStaleMs,
		LastEventMs: p.agent.LastEventMs, OutputLines: outputLines,
	})
}

// drawWhere draws the "where" line at (x, y): <repo>:<folder> $ ▌, typed out
// to WhereChars of whereFrames.
func drawWhere(p *rowPlan, g *cells.Grid, x, y int) {
	repo, folder := cards.Where(root(p.agent), p.shown)
	loc := repo
	switch {
	case repo == "":
		loc = folder
	case folder != "":
		loc = repo + ":" + folder
	}
	prompt := " $ "
	if loc == "" {
		prompt = "$ "
	}
	parts := []struct {
		s  string
		st cells.Style
	}{
		{loc, cells.Style{Role: cells.RText}},
		{prompt, cells.Style{Role: cells.RDim}},
		{"▌", cells.Style{Role: cells.RBright}},
	}
	total := 0
	for _, pt := range parts {
		total += width(pt.s)
	}
	n := total * min(max(p.f.WhereChars, 0), whereFrames) / whereFrames
	wr := writer{g: g, x: x, y: y, end: g.W - marginX}
	for _, pt := range parts {
		s := prefix(pt.s, n)
		n -= width(s)
		wr.put(s, pt.st)
	}
}

// drawCollapsed draws a collapsed row from line head.
func drawCollapsed(in Input, p *rowPlan, g *cells.Grid, head int) {
	g.Blit(cards.CollapsedRow(p.lineW(), p.agent, p.state(in), p.shown, in.Now), p.cardX(), head)
}
