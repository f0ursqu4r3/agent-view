package tui

import (
	tea "charm.land/bubbletea/v2"

	"agent-view/internal/board"
	"agent-view/internal/store"
)

// key applies a key press, per the spec "Interaction". It returns the
// command of a key that quits or suspends the program, and whether the key
// is one of the model's keys.
func (m *Model) key(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	now := m.nowMs()
	switch msg.String() {
	case "q", "ctrl+c":
		return tea.Quit, true
	case "ctrl+z":
		return tea.Suspend, true
	case "j", "down":
		m.move(now, 1)
	case "k", "up":
		m.move(now, -1)
	case "g":
		m.selectRow(now, 0)
	case "G":
		m.selectRow(now, -1)
	case "enter":
		m.enter()
	case "esc":
		if m.ui.Panel != board.PanelNone {
			m.ui.Panel = board.PanelNone
		} else {
			m.ui.Focus = store.AgentKey{}
			m.thaw = true
		}
	case "space":
		m.pause(now)
	case "l":
		m.ui.TallLog = !m.ui.TallLog
		m.thaw = true
	case "c":
		m.togglePanel(board.PanelCoverage)
	case "?":
		m.togglePanel(board.PanelHelp)
	default:
		return nil, false
	}
	m.fixTop(now)
	return nil, true
}

// move moves the selection d rows down (up when d is negative). With no
// selection, down selects the first row and up the last.
func (m *Model) move(now int64, d int) {
	rows := board.Rows(m.store, m.ui, now)
	if len(rows) == 0 {
		return
	}
	i := -1
	for j, r := range rows {
		if r.Key() == m.ui.Selected {
			i = j
			break
		}
	}
	switch {
	case i < 0 && d > 0:
		i = 0
	case i < 0:
		i = len(rows) - 1
	default:
		i = min(max(i+d, 0), len(rows)-1)
	}
	m.ui.Selected = rows[i].Key()
	m.thaw = true
}

// selectRow selects row i; a negative i counts from the end.
func (m *Model) selectRow(now int64, i int) {
	rows := board.Rows(m.store, m.ui, now)
	if len(rows) == 0 {
		return
	}
	if i < 0 {
		i += len(rows)
	}
	m.ui.Selected = rows[min(max(i, 0), len(rows)-1)].Key()
	m.thaw = true
}

// enter focuses the selected agent, or clears the focus when it is already
// focused or nothing is selected.
func (m *Model) enter() {
	if m.ui.Selected == (store.AgentKey{}) || m.ui.Focus == m.ui.Selected {
		m.ui.Focus = store.AgentKey{}
	} else {
		m.ui.Focus = m.ui.Selected
	}
	m.thaw = true
}

// pause pauses or resumes the board and the log. The store keeps applying
// events meanwhile. Resume shows the live state at once: the animation
// state is dropped, so effects stopped by the pause and rows that changed
// height do not play.
func (m *Model) pause(now int64) {
	m.ui.Paused = !m.ui.Paused
	if m.ui.Paused {
		return
	}
	for k := range m.known {
		m.anim.Forget(k)
	}
	m.checkHosts(now)
}

// togglePanel opens a panel, or closes it when it is open.
func (m *Model) togglePanel(p board.Panel) {
	if m.ui.Panel == p {
		m.ui.Panel = board.PanelNone
	} else {
		m.ui.Panel = p
	}
}

// click toggles the focus of the agent card under a left click, and selects
// that agent. It reports whether the click hit a card.
func (m *Model) click(msg tea.MouseClickMsg) bool {
	if msg.Button != tea.MouseLeft {
		return false
	}
	for _, h := range m.hits {
		if msg.X < h.X || msg.X >= h.X+h.W || msg.Y < h.Y || msg.Y >= h.Y+h.H {
			continue
		}
		m.ui.Selected = h.Key
		if m.ui.Focus == h.Key {
			m.ui.Focus = store.AgentKey{}
		} else {
			m.ui.Focus = h.Key
		}
		m.thaw = true
		m.fixTop(m.nowMs())
		return true
	}
	return false
}
