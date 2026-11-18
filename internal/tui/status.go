package tui

import (
	"fmt"

	"agent-view/internal/cells"
	"agent-view/internal/text"
)

// drawStatus draws the status bar on line 0: the product name, one segment
// per host, the counts, and on the right PAUSED and the clock.
func drawStatus(g *cells.Grid, st Status, paused bool) {
	dim := cells.Style{Role: cells.RDim}
	bright := cells.Style{Role: cells.RBright}

	right := g.W - marginX
	clock := text.Clean(st.Clock)
	if clock != "" {
		right -= g.Text(right-width(clock), 0, width(clock), clock, dim)
		right -= 2
	}
	if paused {
		const word = " PAUSED "
		right -= g.Text(right-width(word), 0, width(word), word, cells.Style{Role: cells.RSearch, Bold: true, Inverse: true})
		right -= 2
	}

	wr := writer{g: g, x: marginX, y: 0, end: right}
	wr.put("AGENT VIEW", cells.Style{Role: cells.RBright, Bold: true})
	for _, h := range st.Hosts {
		word, role := hostState(h)
		wr.put("  "+text.Clean(h.Label)+" ", cells.Style{Role: cells.RText})
		wr.put("● "+word, cells.Style{Role: role})
	}
	count := func(n int, one, many string, role cells.Role) {
		label := many
		if n == 1 {
			label = one
		}
		wr.put("  ", dim)
		wr.put(fmt.Sprint(n), cells.Style{Role: role})
		wr.put(" "+label, dim)
	}
	count(st.Agents, "agent", "agents", bright.Role)
	count(st.Working, "working", "working", bright.Role)
	count(st.CallsPerMin, "call/min", "calls/min", bright.Role)
	faults := bright.Role
	if st.Faults > 0 {
		faults = cells.RFail
	}
	count(st.Faults, "fault", "faults", faults)
}

// hostState returns the state word and color of a host segment. Degraded
// sources replace "up".
func hostState(h HostStatus) (string, cells.Role) {
	switch {
	case h.State == "up" && h.Degraded > 0:
		return degradedText(h.Degraded), cells.RSearch
	case h.State == "up":
		return "up", cells.ROK
	case h.State == "down":
		return "down", cells.RFail
	case h.State == "connecting":
		return "connecting", cells.RSearch
	}
	return text.Clean(h.State), cells.RDim
}

// degradedText returns the state word of a host with n degraded sources.
func degradedText(n int) string {
	if n == 1 {
		return "1 source degraded"
	}
	return fmt.Sprintf("%d sources degraded", n)
}

// keyItem is one entry of the key bar. Entries with a lower drop rank go
// first when the bar does not fit.
type keyItem struct {
	key, word string
	drop      int
}

var keyItems = []keyItem{
	{"space", "pause", 7},
	{"j/k", "select", 4},
	{"enter", "focus", 5},
	{"esc", "clear", 1},
	{"c", "coverage", 3},
	{"l", "log", 2},
	{"?", "help", 6},
	{"q", "quit", 8},
}

// drawKeys draws the key bar on line y. Entries are dropped, lowest rank
// first, until the bar fits; "q quit" stays.
func drawKeys(g *cells.Grid, y int, paused bool) {
	items := append([]keyItem(nil), keyItems...)
	if paused {
		items[0].word = "resume"
	}
	fits := func() bool {
		n := 0
		for i, it := range items {
			if i > 0 {
				n += 2
			}
			n += width(it.key) + 1 + width(it.word)
		}
		return n <= g.W-2*marginX
	}
	for !fits() && len(items) > 1 {
		low := 0
		for i, it := range items {
			if it.drop < items[low].drop {
				low = i
			}
		}
		items = append(items[:low], items[low+1:]...)
	}
	wr := writer{g: g, x: marginX, y: y, end: g.W - marginX}
	for i, it := range items {
		if i > 0 {
			wr.put("  ", cells.Style{Role: cells.RDim})
		}
		wr.put(it.key, cells.Style{Role: cells.RBright, Bold: true})
		wr.put(" "+it.word, cells.Style{Role: cells.RDim})
	}
}
