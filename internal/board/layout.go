// Package board decides which agent rows the board shows, in what order, full
// or collapsed, where they sit, how the board scrolls, and the tree-line
// gutters. The composer draws from it.
package board

import (
	"agent-view/internal/cards"
	"agent-view/internal/store"
)

// Panel is the overlay panel the board shows, if any.
type Panel uint8

// The panels.
const (
	PanelNone Panel = iota
	PanelCoverage
	PanelHelp
)

// HistoryPosition anchors the top line to a call as new entries arrive.
// A nil call follows the bottom of the history.
type HistoryPosition struct {
	Call *store.HistoryCall
	Line int // zero for the header, then the wrapped target lines
}

// UI is the interface state that affects the layout.
type UI struct {
	// Selected and Focus are agent keys; the zero key means none.
	Selected, Focus          store.AgentKey
	Paused, TallLog, Reduced bool
	History                  bool
	HistoryTop               HistoryPosition
	Panel                    Panel
	Top                      int
}

// Row is one agent row of the board.
type Row struct {
	AgentID, Host string
	// HostHeader is the host label on the first row of a host group when the
	// board shows more than one host. The header takes the row's first line.
	HostHeader string
	// Sub is true when the agent's parent is on the board. LastSib is true for
	// a sub-agent row that no later row shares a parent with. SessionLink is
	// true when the parent link is "session".
	Sub, LastSib, SessionLink, Full bool
	// Y is the row's top line on the unscrolled board. H is its height in
	// lines: 9 (full) or 1 (collapsed), plus 1 with a host header. The
	// focused row is taller when its call card grows for command output.
	Y, H int
}

// Key returns the key of the row's agent.
func (r Row) Key() store.AgentKey { return store.AgentKey{Host: r.Host, ID: r.AgentID} }

const (
	fullHeight      = 9
	collapsedHeight = 1
	cardHeight      = 7
	recentEndedMs   = 60_000
)

// Rows lays out the board: the store's lane order, with each row full or
// collapsed. A row is full when the agent has an open call, had a call end in
// the last 60 s, is selected, or is focused. The focused row is as high as
// its call card with MaxOutputLines, plus the "where" line and a blank line.
func Rows(s *store.Store, ui UI, now int64) []Row {
	order := s.LaneOrder(now)
	present := make(map[store.AgentKey]bool, len(order))
	for _, k := range order {
		present[k] = true
	}

	rows := make([]Row, 0, len(order))
	parents := make([]store.AgentKey, 0, len(order)) // key of the parent of each row, the zero key for none
	hosts := map[string]bool{}
	for _, k := range order {
		a, ok := s.Agent(k)
		if !ok {
			continue
		}
		r := Row{AgentID: a.ID, Host: a.Host}
		var parent store.AgentKey
		if a.ParentID != nil && present[store.AgentKey{Host: a.Host, ID: *a.ParentID}] { // a parent is on the host of its child
			parent = store.AgentKey{Host: a.Host, ID: *a.ParentID}
			r.Sub = true
			r.SessionLink = a.Link == "session"
		}
		r.Full = k == ui.Selected || k == ui.Focus || isActive(s, k, now)
		rows = append(rows, r)
		parents = append(parents, parent)
		hosts[a.Host] = true
	}

	for i := range rows {
		if rows[i].Sub {
			rows[i].LastSib = true
			for j := i + 1; j < len(rows); j++ {
				if rows[j].Sub && parents[j] == parents[i] {
					rows[i].LastSib = false
					break
				}
			}
		}
	}

	y := 0
	for i := range rows {
		r := &rows[i]
		if len(hosts) > 1 && (i == 0 || rows[i-1].Host != r.Host) {
			r.HostHeader = r.Host
		}
		r.H = collapsedHeight
		switch {
		case r.Full && r.Key() == ui.Focus:
			r.H = focusedHeight(s, r.Key())
		case r.Full:
			r.H = fullHeight
		}
		if r.HostHeader != "" {
			r.H++
		}
		r.Y = y
		y += r.H
	}
	return rows
}

// focusedHeight returns the height of the focused agent's full row: its
// call card (the newest open call, else the newest ended call) as the
// focused card draws it, then the "where" line and a blank line.
func focusedHeight(s *store.Store, k store.AgentKey) int {
	var shown *store.ViewCall
	if open := s.OpenCalls(k); len(open) > 0 {
		shown = open[0]
	} else if ended := s.RecentEnded(k, 1); len(ended) > 0 {
		shown = ended[0]
	}
	return cards.CallCardHeight(shown, cards.MaxOutputLines) + fullHeight - cards.CallH
}

// isActive reports whether the agent has an open call or a call that ended
// within the last 60 s. A call that ended exactly 60 s ago still counts.
func isActive(s *store.Store, k store.AgentKey, now int64) bool {
	if len(s.OpenCalls(k)) > 0 {
		return true
	}
	if ended := s.RecentEnded(k, 1); len(ended) > 0 {
		return now-ended[0].EndMs <= recentEndedMs
	}
	return false
}

// HistoryCount returns how many history cards fit beside the call card:
// min(6, max(0, (width-x+1)/15)) where x is the column after the call card,
// indent+54. The indent is 5 for a sub-agent row and 0 otherwise.
func HistoryCount(width int, sub bool) int {
	x := 54
	if sub {
		x += 5
	}
	return min(6, max(0, (width-x+1)/15))
}

// Scroll returns the new top line that keeps the selected row fully inside
// [top, top+height). It returns top unchanged when no row is selected (the
// zero key). A row taller than the view gets its top aligned.
func Scroll(rows []Row, height, top int, selected store.AgentKey) int {
	if selected == (store.AgentKey{}) {
		return top
	}
	for _, r := range rows {
		if r.Key() != selected {
			continue
		}
		switch {
		case r.H > height || r.Y < top:
			return r.Y
		case r.Y+r.H > top+height:
			return r.Y + r.H - height
		}
		return top
	}
	return top
}

const (
	vertical = "  │"
	dashed   = "  ┆"
	tee      = "  ├──"
	corner   = "  └──"
)

// Gutter returns the gutter strings of row i, one per line of the row. Each
// holds only the gutter columns 0-4. A sub-agent row has a vertical above its
// card's middle line, a tee (or corner, for the last sibling) on it, and a
// vertical below unless it is the last sibling. A session link uses a dashed
// vertical. A main row has an empty gutter, except for a vertical on the lines
// below its card band when its first sub-agent follows. It returns nil when i
// is out of range.
func Gutter(rows []Row, i int) []string {
	if i < 0 || i >= len(rows) {
		return nil
	}
	r := rows[i]
	out := make([]string, r.H)
	head := 0
	if r.HostHeader != "" {
		head = 1
	}

	if !r.Sub {
		if r.Full && i+1 < len(rows) && rows[i+1].Sub {
			line := vertical
			if rows[i+1].SessionLink {
				line = dashed
			}
			for l := head + cardHeight; l < r.H; l++ {
				out[l] = line
			}
		}
		return out
	}

	line := vertical
	if r.SessionLink {
		line = dashed
	}
	mid := tee
	if r.LastSib {
		mid = corner
	}
	if !r.Full {
		out[head] = mid
		return out
	}
	middle := head + cardHeight/2
	for l := head; l < r.H; l++ {
		switch {
		case l == middle:
			out[l] = mid
		case l < middle || !r.LastSib:
			out[l] = line
		}
	}
	return out
}
