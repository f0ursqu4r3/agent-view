package tui

import (
	"fmt"
	"slices"

	"agent-view/internal/feed"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

const (
	// localHost labels the local collector: a StateMsg without a host, and
	// the status bar while no host is known.
	localHost = "local"
	// stateConnecting is the state of a host that has no state of its own.
	stateConnecting = "connecting"
)

// liveChange is a live store change and whether its call failed.
type liveChange struct {
	c      store.Change
	failed bool
}

// applyBatch applies a batch of envelopes to the store. Live changes feed
// the log and the animations, unless the board is paused; history changes
// feed neither. Then it releases unattributed touches, writes due host
// lines, forgets agents that left the board, and keeps the selection in
// view. An agent whose changes in the batch are all history gets its row
// height settled, so the row does not grow or shrink on screen.
func (m *Model) applyBatch(envs feed.EnvelopesMsg) {
	now := m.nowMs()
	var keys []store.AgentKey
	var before map[store.AgentKey]*store.ViewCall
	if !m.ui.Paused {
		keys = callAgents(envs)
		before = m.shownCalls(keys)
	}
	var live []liveChange
	histOnly := map[store.AgentKey]bool{} // agent → no live change in this batch
	for _, e := range envs {
		if e.Kind == "hello" && e.Host != "" {
			if h := m.host(e.Host); h.helloMs == 0 {
				h.helloMs = now
			}
		}
		changes := m.store.Apply(e)
		failed := e.Kind == "tool.end" && e.Ok != nil && !*e.Ok
		m.summaryAt(e.Host, changes, now)
		for _, c := range changes {
			if c.History {
				if _, ok := histOnly[c.Key()]; !ok {
					histOnly[c.Key()] = true
				}
				m.host(c.Host).histMs = now
				continue
			}
			histOnly[c.Key()] = false
			switch {
			case c.Kind == store.CallStarted:
				m.starts = append(m.starts, now)
			case c.Kind == store.CallEnded && failed:
				m.fails = append(m.fails, now)
			}
			if !m.ui.Paused {
				m.log.FromChange(m.store, c)
				live = append(live, liveChange{c, failed})
			}
		}
	}
	if len(live) > 0 {
		after := m.shownCalls(keys)
		// A batch can contain several starts, parallel ends, and replayed
		// history. Animate only the transition the viewer actually sees.
		type transition struct{ changed, started, failed bool }
		display := make(map[store.AgentKey]transition)
		for _, lc := range live {
			c := lc.c
			k := c.Key()
			m.known[k] = true
			next, old := after[k], before[k]
			move := display[k]
			switch c.Kind {
			case store.CallStarted:
				if next != nil && next.CallID == c.CallID && (old == nil || old.CallID != next.CallID) {
					m.anim.OnChange(c, true)
					move.changed, move.started = true, true
				}
			case store.CallEnded:
				if next != nil && old != nil && old.CallID == c.CallID && next.CallID != old.CallID {
					move.changed = true
				}
				if lc.failed && next != nil && next.CallID == c.CallID {
					move.failed = true
				}
			case store.AgentAdded:
				a, ok := m.store.Agent(k)
				m.anim.OnAgentAdded(k, ok && a.ParentID != nil)
			case store.AgentStatus:
				if c.Status == "interrupted" {
					m.anim.OnStopped(k)
				}
			}
			if move.changed || move.failed {
				display[k] = move
			}
		}
		for k, move := range display {
			if move.changed {
				m.displayCall(k, before[k], after[k], move.started)
			}
			if move.failed {
				m.anim.OnFail(k)
			}
		}
	}
	for k, only := range histOnly {
		if only {
			m.anim.Settle(k)
		}
	}
	if n := m.store.Panics - m.panics; n > 0 {
		m.panics = m.store.Panics
		m.addDiag(fmt.Sprintf("store: dropped %d envelope(s) that failed to apply", n))
	}
	m.takeTouches(now)
	m.checkHosts(now)
	m.prune(now)
	m.fixTop(now)
}

func fileWrite(c *store.ViewCall) bool {
	return c != nil && (c.Category == "write" || c.Category == "edit")
}

func (m *Model) displayCall(k store.AgentKey, old, next *store.ViewCall, started bool) {
	// Overlapping writes, or writes separated by at most one second,
	// belong to the same burst for this agent.
	write := fileWrite(next)
	burstStart := started && write && (!fileWrite(old) || (old.Ended && next.StartMs-old.EndMs > 1000))
	historyID := ""
	if old != nil && old.Ended {
		head := m.store.RecentEnded(k, 2)
		if next.Ended && len(head) > 0 {
			head = head[1:]
		}
		if len(head) > 0 && head[0].CallID == old.CallID {
			historyID = old.CallID
		}
	}
	m.anim.OnDisplay(k, write, burstStart, historyID)
}

// callAgents returns the agents whose calls a batch may start or end. An agent
// is on the host of the envelope.
func callAgents(envs []schema.Envelope) []store.AgentKey {
	var keys []store.AgentKey
	add := func(host, id string) {
		k := store.AgentKey{Host: host, ID: id}
		if id != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, e := range envs {
		switch e.Kind {
		case "tool.start":
			if e.Call != nil {
				add(e.Host, e.Call.AgentID)
			}
		case "tool.end":
			add(e.Host, e.AgentID)
		case "snapshot":
			for _, c := range e.OpenCalls {
				add(e.Host, c.AgentID)
			}
		}
	}
	return keys
}

// shownCalls returns the call on the call card of each agent: the newest
// open call, else the newest ended one, else nil.
func (m *Model) shownCalls(keys []store.AgentKey) map[store.AgentKey]*store.ViewCall {
	out := make(map[store.AgentKey]*store.ViewCall, len(keys))
	for _, k := range keys {
		if open := m.store.OpenCalls(k); len(open) > 0 {
			out[k] = open[0]
		} else if ended := m.store.RecentEnded(k, 1); len(ended) > 0 {
			out[k] = ended[0]
		}
	}
	return out
}

// host returns the tracking of a host.
func (m *Model) host(label string) *hostInfo {
	h := m.hosts[label]
	if h == nil {
		h = &hostInfo{}
		m.hosts[label] = h
	}
	return h
}

// summaryAt writes a host's startup history summary at its first live
// change, before the line of that change, unless checkHosts wrote it after
// a quiet period first. The counts leave out what the live change itself
// added. While paused, the summary waits.
func (m *Model) summaryAt(label string, changes []store.Change, now int64) {
	if label == "" {
		return
	}
	h := m.host(label)
	if h.summarized {
		return
	}
	liveAny := false
	var agents, calls int
	for _, c := range changes {
		if c.History {
			continue
		}
		liveAny = true
		switch c.Kind {
		case store.AgentAdded:
			agents++
		case store.CallStarted:
			calls++
		}
	}
	if !liveAny {
		return
	}
	if m.ui.Paused {
		h.due = true
		return
	}
	a, n := m.store.HostStats(label)
	m.summarize(label, a-agents, n-calls, now)
}

func (m *Model) summarize(label string, agents, calls int, now int64) {
	m.log.Host(now, label, fmt.Sprintf("loaded %d agents, %d calls from logs", agents, calls))
	h := m.host(label)
	h.summarized, h.due = true, false
}

// checkHosts writes the host lines that are due: a summary once the host
// has had no history change for 2 s after its hello, so the scan is loaded
// (or held by a pause), and a change of the degraded sources.
func (m *Model) checkHosts(now int64) {
	if m.ui.Paused {
		return
	}
	for _, label := range m.store.Hosts() {
		h := m.host(label)
		if !h.summarized && (h.due || h.helloMs != 0 && now-max(h.helloMs, h.histMs) >= quietMs) {
			a, n := m.store.HostStats(label)
			m.summarize(label, a, n, now)
		}
		if d := m.degraded(label); d != h.degraded {
			h.degraded = d
			word := "up"
			if d > 0 {
				word = degradedText(d)
			}
			m.log.Host(now, label, word)
		}
	}
}

// degraded returns the number of a host's sources that are not ok.
func (m *Model) degraded(label string) int {
	cov, _ := m.store.Coverage(label)
	n := 0
	for _, src := range cov.Sources {
		if src.State != "ok" {
			n++
		}
	}
	return n
}

// takeTouches releases the held unattributed touches into the log. While
// paused they are dropped, so resume replays nothing.
func (m *Model) takeTouches(now int64) {
	if t := m.store.TakeUnattributed(now); len(t) > 0 && !m.ui.Paused {
		m.log.AddTouches(t, now)
	}
}

// setState applies the collector state of a host. The state is keyed by the
// host label as the message carries it, the same string the store uses for
// that host; labels are cleaned only where they are drawn. The bridge always
// sets the label. An empty one is shown as "local" and, like any label,
// touches no other host's state. A change writes a HOST line; a detail goes
// to the diagnostics, so the coverage panel shows it. The bridge sends every
// host state again after a resync, so a state and detail that equal the
// host's last message add no second diagnostic. A host without an earlier
// message counts as connecting with no detail.
func (m *Model) setState(msg feed.StateMsg) {
	host, state, detail := msg.Host, text.Clean(msg.State), text.Clean(msg.Detail)
	if host == "" {
		host = localHost
	}
	prev, ok := m.hostState[host]
	if !ok {
		prev = stateConnecting
	}
	prevDetail := m.hostDetail[host]
	m.hostState[host] = state
	m.hostDetail[host] = detail
	if detail != "" && (state != prev || detail != prevDetail) {
		m.addDiag(host + " collector " + state + ": " + detail) // addDiag cleans the line
	}
	if state != prev && !m.ui.Paused {
		m.log.Host(m.nowMs(), host, state) // Host cleans the label
	}
}

// segments returns the status bar segment of each host: the store's hosts,
// then hosts known only from a StateMsg, else the local collector. A label is
// the raw label; the status bar cleans it when it draws it.
func (m *Model) segments() []HostStatus {
	labels := m.store.Hosts()
	var extra []string
	for h := range m.hostState {
		if !slices.Contains(labels, h) {
			extra = append(extra, h)
		}
	}
	slices.Sort(extra)
	labels = append(labels, extra...)
	if len(labels) == 0 {
		labels = []string{localHost}
	}
	out := make([]HostStatus, 0, len(labels))
	for _, l := range labels {
		st, ok := m.hostState[l]
		if !ok {
			st = stateConnecting
		}
		out = append(out, HostStatus{Label: l, State: st, Degraded: m.degraded(l)})
	}
	return out
}
