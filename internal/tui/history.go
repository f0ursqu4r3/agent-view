package tui

import (
	"fmt"
	"sort"
	"time"

	"github.com/rivo/uniseg"

	"agent-view/internal/board"
	"agent-view/internal/cards"
	"agent-view/internal/cells"
	"agent-view/internal/ordered"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

func historyWidth(w int) int    { return min(54, max(28, w/3)) }
func historyHeight(h int) int   { return h - boardTop - 1 }
func historyPageSize(h int) int { return historyHeight(h) - 5 }

// commandLine consumes one visual line. A negative next offset means the
// command ended. An offset equal to len(s) preserves a final empty line.
func commandLine(s string, width int) (line string, next int) {
	used := 0
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		start, end := gr.Positions()
		if gr.Str() == "\n" {
			return s[:start], end
		}
		used += cells.GraphemeWidth(gr.Str())
		if used > width {
			return s[:start], start
		}
	}
	return s, -1
}

// Cache wrapped commands across animation frames. Calls can receive a new
// target from a snapshot, and the sidebar width changes on terminal resize.
type wrappedCommand struct {
	source string
	width  int
	lines  []string
}

type historyCache = ordered.LRU[*store.HistoryCall, wrappedCommand]

type historyView struct {
	calls []*store.HistoryCall
	width int
	cache *historyCache
}

func (v historyView) lines(c *store.HistoryCall) []string {
	if c.Category != "command" {
		return []string{c.Target}
	}
	if v.cache != nil {
		if cached, ok := v.cache.Get(c); ok && cached.width == v.width && cached.source == c.Target {
			return cached.lines
		}
	}
	var lines []string
	for s := c.Target; ; {
		line, next := commandLine(s, v.width)
		lines = append(lines, line)
		if next < 0 {
			break
		}
		s = s[next:]
	}
	if v.cache != nil {
		v.cache.Set(c, wrappedCommand{c.Target, v.width, lines})
	}
	return lines
}

type historyRow struct{ index, line int }

// move advances by visual lines. The position after the last call is valid.
func (v historyView) move(p historyRow, delta int) historyRow {
	p.line += delta
	for p.line < 0 && p.index > 0 {
		p.index--
		p.line += 1 + len(v.lines(v.calls[p.index]))
	}
	p.line = max(0, p.line)
	for p.index < len(v.calls) {
		height := 1 + len(v.lines(v.calls[p.index]))
		if p.line < height {
			return p
		}
		p.line -= height
		p.index++
	}
	return historyRow{index: len(v.calls)}
}

func (v historyView) bottom(page int) historyRow {
	return v.move(historyRow{index: len(v.calls)}, -page)
}

func (p historyRow) before(q historyRow) bool {
	return p.index < q.index || (p.index == q.index && p.line < q.line)
}

func (v historyView) top(anchor board.HistoryPosition, bottom historyRow) historyRow {
	if anchor.Call != nil {
		// Start times are sorted, but simultaneous calls can have equal times.
		i := sort.Search(len(v.calls), func(i int) bool { return v.calls[i].StartMs >= anchor.Call.StartMs })
		for ; i < len(v.calls) && v.calls[i].StartMs == anchor.Call.StartMs; i++ {
			if v.calls[i] == anchor.Call {
				p := historyRow{i, min(anchor.Line, len(v.lines(anchor.Call)))}
				if p.before(bottom) {
					return p
				}
				break
			}
		}
	}
	return bottom
}

func (m *Model) historyView() historyView {
	return historyView{m.store.CallHistory(m.ui.Selected), historyWidth(m.w) - 4, m.historyCache}
}

func (m *Model) scrollHistory(delta int) bool {
	if !m.ui.History || m.ui.Panel != board.PanelNone || m.w < MinW || m.h < MinH {
		return false
	}
	v := m.historyView()
	bottom := v.bottom(historyPageSize(m.h))
	top := v.top(m.ui.HistoryTop, bottom)
	next := v.move(top, delta)
	if !next.before(bottom) {
		next = bottom
	}
	if next == top {
		return false
	}
	m.ui.HistoryTop = board.HistoryPosition{}
	if next.before(bottom) {
		m.ui.HistoryTop = board.HistoryPosition{Call: v.calls[next.index], Line: next.line}
	}
	m.thaw = true
	return true
}

func historyPanel(in Input, w, h int) *cells.Grid {
	g := panelBox(w, h, "Call history", "PgUp/PgDn")
	dim := cells.Style{Role: cells.RDim}
	if in.Store == nil {
		g.Text(2, 2, w-4, "Select an agent", dim)
		return g
	}
	a, ok := in.Store.Agent(in.UI.Selected)
	if !ok {
		g.Text(2, 2, w-4, "Select an agent", dim)
		return g
	}
	g.Text(2, 1, w-4, text.Clean(a.Name)+" · "+text.Clean(a.Host), cells.Style{Role: cells.RBright, Bold: true})
	v := historyView{in.Store.CallHistory(in.UI.Selected), w - 4, in.historyCache}
	if len(v.calls) == 0 {
		g.Text(2, 3, w-4, "No calls yet", dim)
		return g
	}
	bottom := v.bottom(h - 5)
	top := v.top(in.UI.HistoryTop, bottom)
	y, last := 3, top.index
	for i := top.index; i < len(v.calls) && y < h-2; i++ {
		last = i
		line := 0
		if i == top.index {
			line = top.line
		}
		c := v.calls[i]
		if line == 0 {
			drawHistoryHeader(g, y, c)
			y++
			line++
		}
		for _, body := range v.lines(c)[line-1:] {
			if y == h-2 {
				break
			}
			g.Text(2, y, v.width, body, cells.Style{Role: cells.RText})
			y++
		}
	}
	footer := fmt.Sprintf("%d–%d of %d", top.index+1, last+1, len(v.calls))
	if top.before(bottom) {
		footer += "  ↓ more"
	}
	g.Text(2, h-2, w-4, footer, dim)
	return g
}

func drawHistoryHeader(g *cells.Grid, y int, c *store.HistoryCall) {
	dim := cells.Style{Role: cells.RDim}
	line := writer{g: g, x: 2, y: y, end: g.W - 2}
	line.put(time.UnixMilli(c.StartMs).Format("15:04:05")+" ", dim)
	line.put(schema.Label(c.Category)+" ", cells.Style{Role: cells.RoleOf(c.Category)})
	result := c.Result()
	style := dim
	switch result {
	case "fail":
		style.Role = cells.RFail
	case "ok":
		style.Role = cells.ROK
	}
	line.put(result, style)
	if c.EndMs != 0 {
		line.put(" "+cards.Elapsed(c.EndMs-c.StartMs), dim)
	}
}
