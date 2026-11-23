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
		if m.ui.Selected == (store.AgentKey{}) && len(m.hits) > 0 {
			m.setSelection(m.hits[0].Key)
		}
		m.enter()
	case "h":
		if m.ui.Panel != board.PanelNone {
			return nil, false
		}
		m.ui.History = !m.ui.History
		m.ui.HistoryTop = board.HistoryPosition{}
		if m.ui.History && m.ui.Selected == (store.AgentKey{}) && len(m.hits) > 0 {
			m.setSelection(m.hits[0].Key)
		}
		m.thaw = true
	case "[", "]":
		const step = 4
		width := historyWidth(m.w, m.ui.HistoryWidth)
		if msg.String() == "[" {
			width -= step
		} else {
			width += step
		}
		if !m.resizeHistory(width) {
			return nil, false
		}
	case "pgdown", "pgup":
		step := historyPageSize(m.h)
		if msg.String() == "pgup" {
			step = -step
		}
		if !m.scrollHistory(step) {
			return nil, false
		}
	case "esc":
		if m.ui.Panel != board.PanelNone {
			m.ui.Panel = board.PanelNone
		} else if m.ui.History {
			m.ui.History = false
			m.thaw = true
		} else {
			m.ui.Focus = store.AgentKey{}
			m.thaw = true
		}
	case "b":
		if !m.ui.Reduced {
			m.motion.background = (m.motion.background + 1) % backgroundCount
			m.motion.phase = 0
			m.thaw = true
		}
	case "r":
		if !m.ui.Reduced && !m.ui.Paused {
			m.motion.title = 19
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
	m.manualScroll = false
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
	m.setSelection(rows[i].Key())
	m.thaw = true
}

// selectRow selects row i; a negative i counts from the end.
func (m *Model) selectRow(now int64, i int) {
	m.manualScroll = false
	rows := board.Rows(m.store, m.ui, now)
	if len(rows) == 0 {
		return
	}
	if i < 0 {
		i += len(rows)
	}
	m.setSelection(rows[min(max(i, 0), len(rows)-1)].Key())
	m.thaw = true
}

func (m *Model) setSelection(key store.AgentKey) {
	if m.ui.Selected != key {
		m.ui.Selected = key
		m.ui.HistoryTop = board.HistoryPosition{}
	}
}

// enter focuses the selected agent, or clears the focus when it is already
// focused or nothing is selected.
func (m *Model) enter() {
	m.manualScroll = false
	m.motion.avatar = 0
	if m.ui.Selected == (store.AgentKey{}) || m.ui.Focus == m.ui.Selected {
		m.ui.Focus = store.AgentKey{}
	} else {
		m.ui.Focus = m.ui.Selected
		if !m.ui.Paused && !m.ui.Reduced {
			m.motion.avatar = 10
		}
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
		m.motion.avatar, m.motion.title = 0, 0
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

// A left click on the history border starts a drag. A click on an agent card selects and focuses it.
func (m *Model) click(msg tea.MouseClickMsg) bool {
	if msg.Button != tea.MouseLeft {
		return false
	}
	if m.canResizeHistory() && msg.X == m.w-historyWidth(m.w, m.ui.HistoryWidth) &&
		msg.Y >= boardTop && msg.Y < boardTop+historyHeight(m.h) {
		m.draggingHistory = true
		return true
	}
	for _, h := range m.hits {
		if msg.X < h.X || msg.X >= h.X+h.W || msg.Y < h.Y || msg.Y >= h.Y+h.H {
			continue
		}
		m.setSelection(h.Key)
		m.enter()
		m.fixTop(m.nowMs())
		return true
	}
	return false
}

func (m *Model) wheel(msg tea.MouseWheelMsg) bool {
	bh := boardHeight(m.h, m.ui.TallLog)
	if m.ui.Panel != board.PanelNone || m.w < MinW || m.h < MinH || msg.X < 0 || msg.X >= m.w || msg.Y < boardTop {
		return false
	}
	const linesPerNotch = 3
	delta := linesPerNotch
	switch msg.Button {
	case tea.MouseWheelUp:
		delta = -delta
	case tea.MouseWheelDown:
	default:
		return false
	}
	if m.ui.History && msg.X >= m.w-historyWidth(m.w, m.ui.HistoryWidth) {
		return msg.Y < boardTop+historyHeight(m.h) && m.scrollHistory(delta)
	}
	if msg.Y >= boardTop+bh {
		return false
	}
	old := m.ui.Top
	m.ui.Top += delta
	previousMode := m.manualScroll
	m.manualScroll = true
	m.fixTop(m.nowMs())
	if m.ui.Top == old {
		m.manualScroll = previousMode
		return false
	}
	m.thaw = true
	return true
}
