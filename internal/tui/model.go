package tui

import (
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/anim"
	"agent-view/internal/board"
	"agent-view/internal/cards"
	"agent-view/internal/cells"
	"agent-view/internal/feed"
	"agent-view/internal/ordered"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// Ticks and limits of the model.
const (
	frameEvery = 50 * time.Millisecond  // animation frames, while something animates
	spinEvery  = 125 * time.Millisecond // spinner and elapsed bar, while a call is open
	clockEvery = time.Second            // status clock, touches, host summaries

	diagCap = 200    // diagnostics lines kept
	rateMs  = 60_000 // the window of calls per minute
	quietMs = 2_000  // a host's summary goes out after this long without a history change
)

// The tick messages. Each tick re-arms itself only while its condition
// holds; an event batch, a key, or a resize starts a stopped tick again.
type (
	frameMsg struct{}
	spinMsg  struct{}
	clockMsg struct{}
)

// Config configures a Model. Now nil uses the wall clock. LingerMs is the
// window of the faults count; zero or less takes the lifecycle default.
// NoColor encodes frames without color (cells.EncodeNoColor).
type Config struct {
	Store    *store.Store
	Reduced  bool
	NoColor  bool
	Now      func() time.Time
	LingerMs int64
}

// Model is the Bubble Tea model of the TUI. It owns the store, the animation
// engine, the event log, and the interface state.
//
// Update composes the frame after each change, so the engine has seen every
// row before the model decides whether frames go on; View returns that frame.
// While paused, the board is a frozen frame with a live status bar; a key
// that changes the board (selection, focus, log size) or a resize draws it
// again from the store.
type Model struct {
	draggingHistory bool
	historyCache    *historyCache
	store           *store.Store
	anim            *anim.Engine
	log             *board.Log
	now             func() time.Time
	linger          int64
	encode          func(*cells.Grid) string // cells.Encode, or cells.EncodeNoColor

	ui                               board.UI
	w, h                             int
	spin                             int
	motion                           motionState
	motionOn, launched, manualScroll bool

	frameOn, spinOn, clockOn bool // a tick of that kind is on its way

	// hostState is the collector state of each host, keyed by the raw host
	// label, as the store keys its hosts. A host without an entry is
	// connecting.
	hostState map[string]string
	// hostDetail is the detail of the last StateMsg of each host, under the
	// same key, so a repeated message adds no second diagnostic.
	hostDetail map[string]string
	hosts      map[string]*hostInfo
	known      map[store.AgentKey]bool // agents that may have animation state
	starts     []int64                 // arrival times of live call starts
	fails      []int64                 // arrival times of live failed ends
	diag       []string
	panics     int // store panics already reported

	content    string // the last frame, encoded
	hits       []Hit  // the agent cards of the last frame
	frozen     *cells.Grid
	frozenHits []Hit
	thaw       bool // draw the frozen board again
}

var _ tea.Model = (*Model)(nil)

// hostInfo is what the model tracks for one host.
type hostInfo struct {
	helloMs    int64 // arrival of the first hello; 0 for none
	histMs     int64 // arrival of the last history change; 0 for none
	summarized bool  // the "loaded" line went out
	due        bool  // the summary waits for the pause to end
	degraded   int   // the degraded sources last logged
}

// NewModel returns a model on the store. The status shows the collector as
// connecting until the first StateMsg.
func NewModel(c Config) *Model {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	linger := c.LingerMs
	if linger <= 0 {
		linger = store.DefaultLingerMs
	}
	s := c.Store
	if s == nil {
		s = store.New(store.Options{})
	}
	encode := cells.Encode
	if c.NoColor {
		encode = cells.EncodeNoColor
	}
	return &Model{
		store: s, anim: anim.New(c.Reduced), log: &board.Log{}, now: now, linger: linger, encode: encode,
		historyCache: ordered.NewLRU[*store.HistoryCall, wrappedCommand](32),
		ui:           board.UI{Reduced: c.Reduced},
		hostState:    map[string]string{}, hostDetail: map[string]string{}, hosts: map[string]*hostInfo{}, known: map[store.AgentKey]bool{},
	}
}

// Init starts the clock tick.
func (m *Model) Init() tea.Cmd { return m.arm() }

// Update applies one message, composes the frame, and returns the ticks to
// start.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case feed.EnvelopesMsg:
		m.applyBatch(msg)
	case feed.StateMsg:
		m.setState(msg)
	case feed.DiagMsg:
		m.addDiag(string(msg))
	case frameMsg:
		m.frameOn = false
		if !m.ui.Paused {
			m.anim.Step()
		}
	case motionMsg:
		m.motionOn = false
		if !m.ui.Paused {
			m.motion.phase++
			m.motion.title = max(0, m.motion.title-1)
			m.motion.avatar = max(0, m.motion.avatar-1)
		}
	case spinMsg:
		m.spinOn = false
		m.spin++
	case clockMsg:
		m.clockOn = false
		m.tick()
	case tea.WindowSizeMsg:
		m.draggingHistory = false
		m.w, m.h = max(msg.Width, 0), max(msg.Height, 0)
		if !m.launched && !m.ui.Reduced && m.w >= MinW && m.h >= MinH {
			m.launched = true
			m.motion.title = 19
		}
		m.thaw = true
		m.fixTop(m.nowMs())
	case tea.KeyPressMsg:
		m.draggingHistory = false
		cmd, ok := m.key(msg)
		if !ok || cmd != nil {
			return m, cmd
		}
	case tea.MouseWheelMsg:
		if !m.wheel(msg) {
			return m, nil
		}
	case tea.MouseMotionMsg:
		if !m.draggingHistory || msg.Button != tea.MouseLeft || !m.resizeHistory(m.w-msg.X) {
			return m, nil
		}
	case tea.MouseReleaseMsg:
		if !m.draggingHistory {
			return m, nil
		}
		m.draggingHistory = false
		if !m.resizeHistory(m.w - msg.X) {
			return m, nil
		}
	case tea.MouseClickMsg:
		m.draggingHistory = false
		if !m.click(msg) {
			return m, nil
		}
	default:
		return m, nil
	}
	m.render()
	return m, m.arm()
}

// View returns the last composed frame.
func (m *Model) View() tea.View {
	return tea.View{Content: m.content, AltScreen: true, MouseMode: tea.MouseModeCellMotion}
}

// Diagnostics returns the held diagnostics lines, oldest first: collector
// stderr, collector failures, and dropped envelopes.
func (m *Model) Diagnostics() []string { return slices.Clone(m.diag) }

// arm returns the ticks that are needed and not on their way.
func (m *Model) arm() tea.Cmd {
	var cmds []tea.Cmd
	if cmd := m.motionTick(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if !m.clockOn {
		m.clockOn = true
		cmds = append(cmds, tea.Every(clockEvery, func(time.Time) tea.Msg { return clockMsg{} }))
	}
	if !m.frameOn && !m.ui.Paused && m.anim.Active() {
		m.frameOn = true
		cmds = append(cmds, tea.Tick(frameEvery, func(time.Time) tea.Msg { return frameMsg{} }))
	}
	if !m.spinOn && !m.ui.Paused && m.anyOpen(m.nowMs()) {
		m.spinOn = true
		cmds = append(cmds, tea.Tick(spinEvery, func(time.Time) tea.Msg { return spinMsg{} }))
	}
	return tea.Batch(cmds...)
}

func (m *Model) nowMs() int64 { return m.now().UnixMilli() }

// anyOpen reports whether an agent on the board has an open call.
func (m *Model) anyOpen(now int64) bool {
	for _, k := range m.store.LaneOrder(now) {
		if len(m.store.OpenCalls(k)) > 0 {
			return true
		}
	}
	return false
}

// tick runs the work of the clock: touches, host lines, agents that left,
// and the scroll position.
func (m *Model) tick() {
	now := m.nowMs()
	m.takeTouches(now)
	m.checkHosts(now)
	m.prune(now)
	m.fixTop(now)
}

// render composes the frame. While paused, the board stays as it was and
// only the status bar and the panel are drawn again.
func (m *Model) render() {
	now := m.nowMs()
	if m.ui.HistoryTop.Call != nil && m.ui.History && m.w >= MinW && m.h >= MinH && (!m.ui.Paused || m.thaw) {
		v := m.historyView()
		bottom := v.bottom(historyPageSize(m.h))
		if v.top(m.ui.HistoryTop, bottom) == bottom {
			m.ui.HistoryTop = board.HistoryPosition{}
		}
	}
	in := Input{
		Store: m.store, UI: m.ui, historyCache: m.historyCache, Anim: m.anim, Log: m.log, Status: m.status(now),
		Now: now, Spin: m.spin, motion: m.motion, W: m.w, H: m.h, Diagnostics: m.diag,
	}
	if !m.ui.Paused || m.w < MinW || m.h < MinH {
		var g *cells.Grid
		g, m.hits = Compose(in)
		m.frozen, m.frozenHits, m.thaw = nil, nil, false
		m.content = m.encode(g)
		return
	}
	if m.frozen == nil || m.thaw || m.frozen.W != m.w || m.frozen.H != m.h {
		base := in
		base.UI.Panel = board.PanelNone
		m.frozen, m.frozenHits = Compose(base)
		m.thaw = false
	}
	g := &cells.Grid{W: m.frozen.W, H: m.frozen.H, C: slices.Clone(m.frozen.C)}
	g.Blit(cells.New(g.W, 1), 0, 0)
	drawStatus(g, in.Status, true)
	m.hits = m.frozenHits
	if drawPanel(g, in) {
		m.hits = nil
	}
	m.content = m.encode(g)
}

// status returns the values of the status bar. Agents counts every present
// agent. Working counts the agents whose status is working and whose row does
// not say STALE, so the bar and the rows agree. A stale agent whose row says
// WAITING (a parent whose newest open call is an agent call, with a present
// sub-agent) counts as well, because the store marks it stale and the row does
// not.
func (m *Model) status(now int64) Status {
	st := Status{titleFrame: m.motion.title, Hosts: m.segments(), Paused: m.ui.Paused, Clock: time.UnixMilli(now).Format("15:04:05")}
	agents := m.store.Agents(now)
	var parents map[store.AgentKey]bool // built at the first stale agent
	for _, a := range agents {
		st.Agents++
		if a.Status != "working" {
			continue
		}
		if !a.Stale {
			st.Working++
			continue
		}
		if parents == nil {
			parents = parentKeys(agents)
		}
		if cards.Waiting(a, m.store.OpenCalls(a.Key()), parents[a.Key()]) {
			st.Working++
		}
	}
	m.starts = dropBefore(m.starts, now-rateMs)
	m.fails = dropBefore(m.fails, now-m.linger)
	st.CallsPerMin, st.Faults = len(m.starts), len(m.fails)
	return st
}

// parentKeys returns the keys of the agents that have a sub-agent in agents.
// A parent is on the host of its sub-agent.
func parentKeys(agents []store.ViewAgent) map[store.AgentKey]bool {
	parents := map[store.AgentKey]bool{}
	for _, a := range agents {
		if a.ParentID != nil {
			parents[store.AgentKey{Host: a.Host, ID: *a.ParentID}] = true
		}
	}
	return parents
}

// dropBefore drops the leading times before cutoff from times in arrival
// order.
func dropBefore(times []int64, cutoff int64) []int64 {
	i := 0
	for i < len(times) && times[i] < cutoff {
		i++
	}
	return times[i:]
}

// fixTop scrolls the board to keep the selection visible and clamps the top
// line as the composer does, so it never runs past the end of the board.
func (m *Model) fixTop(now int64) {
	bh := boardHeight(m.h, m.ui.TallLog)
	if m.w < MinW || m.h < MinH || bh <= 0 {
		return
	}
	rows := board.Rows(m.store, m.ui, now)
	if !m.manualScroll {
		m.ui.Top = board.Scroll(rows, bh, m.ui.Top, m.ui.Selected)
	}
	total := 0
	for _, r := range rows {
		total += r.H
	}
	m.ui.Top = min(max(m.ui.Top, 0), max(0, total-bh))
}

// prune forgets the agents that left the board, by host and ID, and clears
// the focus and the selection when their agent left.
func (m *Model) prune(now int64) {
	order := m.store.LaneOrder(now)
	present := make(map[store.AgentKey]bool, len(order))
	for _, k := range order {
		present[k] = true
	}
	for k := range m.known {
		if !present[k] {
			m.anim.Forget(k)
			delete(m.known, k)
		}
	}
	for _, k := range order {
		m.known[k] = true
	}
	if !present[m.ui.Focus] {
		m.ui.Focus = store.AgentKey{}
	}
	if !present[m.ui.Selected] {
		m.setSelection(store.AgentKey{})
	}
}

// addDiag adds a cleaned diagnostics line and drops the oldest beyond 200.
func (m *Model) addDiag(s string) {
	m.diag = append(m.diag, text.Clean(s))
	if n := len(m.diag) - diagCap; n > 0 {
		m.diag = append(m.diag[:0:0], m.diag[n:]...)
	}
}
