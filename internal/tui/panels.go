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
	{"wheel", "scroll the agent board"},
	{"b", "cycle the animated board background"},
	{"r", "replay the title snap"},
	{"esc", "clear the focus, close a panel"},
	{"space", "pause / resume the board and the log"},
	{"l", "toggle the tall log"},
	{"c", "coverage and diagnostics"},
	{"?", "this help"},
	{"ctrl+z", "suspend"},
	{"q", "quit"},
}

// HostCoverage is the coverage of one host, with the host's label.
type HostCoverage struct {
	Label    string
	Coverage schema.Coverage
}

// coverages returns the coverage of every host of the store, in the store's
// order. A host that sent none has an empty coverage.
func coverages(s *store.Store) []HostCoverage {
	if s == nil {
		return nil
	}
	hosts := s.Hosts()
	out := make([]HostCoverage, len(hosts))
	for i, h := range hosts {
		c, _ := s.Coverage(h)
		out[i] = HostCoverage{Label: h, Coverage: c}
	}
	return out
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

// panelLine is one line of the coverage panel. A line has a label in the
// left column and a value in the right one; a wide line is one text from the
// left edge of the panel. A line with no text is blank.
type panelLine struct {
	label, value string
	st           cells.Style
	wide         bool
}

// draw draws the line at line y of a panel w cells wide.
func (l panelLine) draw(g *cells.Grid, y, w int) {
	if l.wide {
		g.Text(2, y, w-4, l.value, l.st)
		return
	}
	g.Text(2, y, panelLabelW-1, l.label, cells.Style{Role: cells.RDim})
	vx := 2 + panelLabelW
	g.Text(vx, y, w-vx-2, l.value, l.st)
}

// coverageLines returns the lines of one host's coverage: what the collector
// watches and how well.
func coverageLines(cov schema.Coverage) []panelLine {
	dim := cells.Style{Role: cells.RDim}
	plain := cells.Style{Role: cells.RText}
	var out []panelLine
	row := func(label string, values []string, styles []cells.Style) {
		if len(values) == 0 {
			values, styles = []string{"none"}, []cells.Style{dim}
		}
		for i, v := range values {
			l := panelLine{value: v, st: styles[i]}
			if i == 0 {
				l.label = label
			}
			out = append(out, l)
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
	return out
}

// diagKept is the number of the newest diagnostic lines the coverage panel
// always shows (fewer when there are fewer).
const diagKept = 4

// coverageBlocks returns the blocks of the coverage panel, in order. With two
// or more hosts a block is one host's coverage after a line "host <label>".
// With none or one host there is one block with no host line (an empty
// coverage when there is no host).
func coverageBlocks(covs []HostCoverage) [][]panelLine {
	if len(covs) < 2 {
		var cov schema.Coverage
		if len(covs) == 1 {
			cov = covs[0].Coverage
		}
		return [][]panelLine{coverageLines(cov)}
	}
	dim := cells.Style{Role: cells.RDim}
	blocks := make([][]panelLine, len(covs))
	for i, hc := range covs {
		host := panelLine{value: "host " + text.Clean(hc.Label), st: dim, wide: true}
		blocks[i] = append([]panelLine{host}, coverageLines(hc.Coverage)...)
	}
	return blocks
}

// moreMarker returns the line that counts what the coverage panel leaves out:
// the lines of the first block, the hosts after it, or both.
func moreMarker(lines, hosts int) string {
	count := func(n int, noun string) string {
		if n == 1 {
			return "1 more " + noun
		}
		return fmt.Sprintf("%d more %ss", n, noun)
	}
	switch {
	case lines > 0 && hosts > 0:
		return "… " + count(lines, "line") + ", " + count(hosts, "host")
	case lines > 0:
		return "… " + count(lines, "line")
	}
	return "… " + count(hosts, "host")
}

// CoveragePanel draws the coverage panel, w × h: what the collector watches
// and how well, then diagnostics. One rule lays out any number of hosts.
//
// The panel keeps the newest diagnostic lines. The section of the diagnostics
// is a blank line, the line "diagnostics", then the diagnostic lines (or
// "none"). It has a reserve of 2 lines plus the newest four diagnostic lines
// (one line when there are none), and the coverage gets the other lines.
//
// With two or more hosts the coverage is one block per host in the order of
// covs, each starting with a line "host <label>", with a blank line between
// blocks. With one host (or none: an empty coverage) there is one block with
// no host line. The panel shows the most whole blocks that fit. When it
// leaves some out, it adds the line "… <n> more hosts" after them. When not
// even the first block fits with that line, the panel shows the first lines
// of the first block and the line "… <m> more lines, <n> more hosts", m being
// the lines of that block left out and n the hosts after it. With one host
// the line reads "… <m> more lines". A count of 1 reads "1 more line" or "1
// more host".
//
// The section of the diagnostics follows the coverage directly and shows the
// newest lines that fit, at least the reserve; older lines drop with no
// marker. When the box is smaller than the reserve, it shows the last lines
// of the section and nothing else. When everything fits, the panel shows it
// all.
func CoveragePanel(covs []HostCoverage, diag []string, w, h int) *cells.Grid {
	g := panelBox(w, h, "coverage", "c or esc to close")
	if w < 6 || h < 3 {
		return g
	}
	dim := cells.Style{Role: cells.RDim}
	plain := cells.Style{Role: cells.RText}
	inner := h - 2

	// section returns the section of the diagnostics with room diagnostic lines.
	section := func(room int) []panelLine {
		out := []panelLine{{}, {value: "diagnostics", st: dim, wide: true}}
		if len(diag) == 0 {
			return append(out, panelLine{value: "none", st: dim, wide: true})
		}
		for _, d := range diag[max(0, len(diag)-room):] {
			out = append(out, panelLine{value: text.Clean(d), st: plain, wide: true})
		}
		return out
	}
	reserve := 2 + max(1, min(diagKept, len(diag)))

	var lines []panelLine
	if inner < reserve {
		lines = section(diagKept)
		lines = lines[len(lines)-inner:]
	} else {
		blocks := coverageBlocks(covs)
		body := inner - reserve
		// Take whole blocks while the next one fits in body with the marker
		// line, which the last block does not need.
		k, used := 0, 0
		for k < len(blocks) {
			next := used + len(blocks[k])
			if k > 0 {
				next++ // a blank line between blocks
			}
			marker := 0
			if k+1 < len(blocks) {
				marker = 1
			}
			if next+marker > body {
				break
			}
			k, used = k+1, next
		}
		for i, b := range blocks[:k] {
			if i > 0 {
				lines = append(lines, panelLine{})
			}
			lines = append(lines, b...)
		}
		linesLeft, hostsLeft := 0, len(blocks)-k
		if k == 0 && body > 0 {
			// No block fits whole: show the first lines of the first block,
			// and leave the last line of body to the marker.
			shown := body - 1
			lines = append(lines, blocks[0][:shown]...)
			linesLeft, hostsLeft = len(blocks[0])-shown, len(blocks)-1
		}
		if k < len(blocks) && body > used { // the marker fits
			lines = append(lines, panelLine{value: moreMarker(linesLeft, hostsLeft), st: dim, wide: true})
		}
		lines = append(lines, section(inner-len(lines)-2)...)
	}
	for i, l := range lines {
		l.draw(g, 1+i, w)
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
