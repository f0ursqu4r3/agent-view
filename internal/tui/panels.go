package tui

import (
	"fmt"
	"slices"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// Panel layout, in cells.
const (
	panelLabelW = 19 // the label column of the coverage panel
	helpKeyW    = 11 // the key column of the help panel
	helpW       = 60
)

// helpKeys holds the lines of the help panel: key, then action.
var helpKeys = [][2]string{
	{"j / ↓", "select the next row"},
	{"k / ↑", "select the previous row"},
	{"g / G", "select the first / last row"},
	{"enter", "focus the selected agent; again to clear"},
	{"click", "focus the clicked agent"},
	{"esc", "clear the focus, close a panel"},
	{"space", "pause / resume the board and the log"},
	{"l", "toggle the tall log"},
	{"c", "coverage and diagnostics"},
	{"?", "this help"},
	{"ctrl+z", "suspend"},
	{"q", "quit"},
}

// coverage returns the coverage of the first host that sent one.
func coverage(s *store.Store) schema.Coverage {
	if s == nil {
		return schema.Coverage{}
	}
	for _, h := range s.Hosts() {
		if c, ok := s.Coverage(h); ok {
			return c
		}
	}
	return schema.Coverage{}
}

// panelBox returns a w × h grid with a bright border, the title at the top
// left, and the close hint at the bottom right.
func panelBox(w, h int, title, hint string) *cells.Grid {
	g := cells.New(w, h)
	border := cells.Style{Role: cells.RBright}
	g.Box(0, 0, w, h, cells.Square, border)
	g.Text(2, 0, w-4, " "+title+" ", cells.Style{Role: cells.RBright, Bold: true})
	if hw := width(hint) + 2; hw <= w-4 {
		g.Text(w-2-hw, h-1, hw, " "+hint+" ", cells.Style{Role: cells.RDim})
	}
	return g
}

// CoveragePanel draws the coverage panel, w × h: what the collector watches
// and how well, then the newest diagnostics lines that fit.
func CoveragePanel(cov schema.Coverage, diag []string, w, h int) *cells.Grid {
	g := panelBox(w, h, "coverage", "c or esc to close")
	if w < 6 || h < 3 {
		return g
	}
	dim := cells.Style{Role: cells.RDim}
	plain := cells.Style{Role: cells.RText}
	y, last := 1, h-2 // the inner lines
	vx := 2 + panelLabelW
	row := func(label string, values []string, styles []cells.Style) {
		if y > last {
			return
		}
		g.Text(2, y, panelLabelW-1, label, dim)
		if len(values) == 0 {
			values, styles = []string{"none"}, []cells.Style{dim}
		}
		for i, v := range values {
			if y > last {
				return
			}
			g.Text(vx, y, w-vx-2, v, styles[i])
			y++
		}
	}
	list := func(label string, items []string) {
		vals := make([]string, len(items))
		sts := make([]cells.Style, len(items))
		for i, it := range items {
			vals[i], sts[i] = text.Clean(it), plain
		}
		row(label, vals, sts)
	}
	one := func(label, v string) { row(label, []string{v}, []cells.Style{plain}) }

	list("log roots", cov.LogRoots)
	one("logs open", fmt.Sprintf("%d of %d discovered", cov.LogsOpen, cov.LogsDiscovered))
	list("watch roots", cov.WatchRoots)
	one("malformed records", fmt.Sprint(cov.MalformedRecords))
	one("fs events dropped", fmt.Sprint(cov.FSDropped))

	names := make([]string, 0, len(cov.Sources))
	for n := range cov.Sources {
		names = append(names, n)
	}
	slices.Sort(names)
	vals := make([]string, len(names))
	sts := make([]cells.Style, len(names))
	for i, n := range names {
		src := cov.Sources[n]
		vals[i] = text.Clean(n) + ": " + text.Clean(src.State)
		if src.Detail != "" {
			vals[i] += " (" + text.Clean(src.Detail) + ")"
		}
		sts[i] = cells.Style{Role: cells.RSearch}
		switch src.State {
		case "ok":
			sts[i].Role = cells.ROK
		case "down":
			sts[i].Role = cells.RFail
		}
	}
	row("sources", vals, sts)

	y++ // a blank line
	if y > last {
		return g
	}
	g.Text(2, y, w-4, "diagnostics", dim)
	y++
	room := last - y + 1
	if len(diag) == 0 && room > 0 {
		g.Text(2, y, w-4, "none", dim)
		return g
	}
	for i, d := range diag[max(0, len(diag)-room):] {
		g.Text(2, y+i, w-4, text.Clean(d), plain)
	}
	return g
}

// HelpPanel draws the help panel, w × h: every key and its action.
func HelpPanel(w, h int) *cells.Grid {
	g := panelBox(w, h, "keys", "esc to close")
	for i, k := range helpKeys {
		y := 1 + i
		if y > h-2 {
			break
		}
		g.Text(2, y, helpKeyW-1, k[0], cells.Style{Role: cells.RBright, Bold: true})
		g.Text(2+helpKeyW, y, w-4-helpKeyW, k[1], cells.Style{Role: cells.RText})
	}
	return g
}
