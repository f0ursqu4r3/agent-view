// Package board decides which agent rows the board shows, in what order, full
// or collapsed, where they sit, how the board scrolls, and the tree-line
// gutters. The composer draws from it.
package board

import (
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

// UI is the interface state that affects the layout.
type UI struct {
	Selected, Focus          string
	Paused, TallLog, Reduced bool
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
	// lines: 9 (full) or 1 (collapsed), plus 1 with a host header.
	Y, H int
}

const (
	fullHeight      = 9
	collapsedHeight = 1
	cardHeight      = 7
	recentEndedMs   = 60_000
)

// Rows lays out the board: the store's lane order, with each row full or
// collapsed. A row is full when the agent has an open call, had a call end in
// the last 60 s, is selected, or is focused.
func Rows(s *store.Store, ui UI, now int64) []Row {
	order := s.LaneOrder(now)
	present := make(map[string]bool, len(order))
	for _, id := range order {
		present[id] = true
	}

	rows := make([]Row, 0, len(order))
	parents := make([]string, 0, len(order)) // parent ID of each row, "" for none
	hosts := map[string]bool{}
	for _, id := range order {
		a, ok := s.Agent(id)
		if !ok {
			continue
		}
		r := Row{AgentID: id, Host: a.Host}
		parent := ""
		if a.ParentID != nil && present[*a.ParentID] {
			parent = *a.ParentID
			r.Sub = true
			r.SessionLink = a.Link == "session"
		}
		r.Full = id == ui.Selected || id == ui.Focus || isActive(s, id, now)
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
		if r.Full {
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

// isActive reports whether the agent has an open call or a call that ended
// within the last 60 s. A call that ended exactly 60 s ago still counts.
func isActive(s *store.Store, id string, now int64) bool {
	if len(s.OpenCalls(id)) > 0 {
		return true
	}
	if ended := s.RecentEnded(id, 1); len(ended) > 0 {
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
// [top, top+height). It returns top unchanged when no row is selected. A row
// taller than the view gets its top aligned.
func Scroll(rows []Row, height, top int, selected string) int {
	if selected == "" {
		return top
	}
	for _, r := range rows {
		if r.AgentID != selected {
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
