package tui

import (
	"fmt"
	"slices"

	"agent-view/internal/feed"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// localHost labels the local collector until a host label is known.
const localHost = "local"

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
	var ids []string
	var before map[string]string
	if !m.ui.Paused {
		ids = callAgents(envs)
		before = m.shownCalls(ids)
	}
	var live []liveChange
	histOnly := map[string]bool{} // agent → no live change in this batch
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
				if _, ok := histOnly[c.AgentID]; !ok {
					histOnly[c.AgentID] = true
				}
				m.host(c.Host).histMs = now
				continue
			}
			histOnly[c.AgentID] = false
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
		after := m.shownCalls(ids)
		for _, lc := range live {
			c := lc.c
			m.known[c.AgentID] = true
			m.animate(c, lc.failed, after[c.AgentID] != before[c.AgentID], after[c.AgentID] == c.CallID)
		}
	}
	for id, only := range histOnly {
		if only {
			m.anim.Settle(id)
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

// animate starts the effects of a live change. shownChanged reports whether
// the batch changed the call on the agent's call card; shown reports whether
// the card shows the change's call after the batch. A failed call flashes
// the card only when the card shows it: a failed older parallel call, or a
// failure followed by a newer start, flashes nothing.
func (m *Model) animate(c store.Change, failed, shownChanged, shown bool) {
	switch c.Kind {
	case store.CallStarted:
		m.anim.OnChange(c, shownChanged)
	case store.CallEnded:
		if failed && shown {
			m.anim.OnFail(c.AgentID)
		}
	case store.AgentAdded:
		a, ok := m.store.Agent(c.AgentID)
		m.anim.OnAgentAdded(c.AgentID, ok && a.ParentID != nil)
	case store.AgentStatus:
		if c.Status == "interrupted" {
			m.anim.OnStopped(c.AgentID) // flashes the STOPPED word once
		}
	}
}

// callAgents returns the agents whose calls a batch may start or end.
func callAgents(envs []schema.Envelope) []string {
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, e := range envs {
		switch e.Kind {
		case "tool.start":
			if e.Call != nil {
				add(e.Call.AgentID)
			}
		case "tool.end":
			add(e.AgentID)
		case "snapshot":
			for _, c := range e.OpenCalls {
				add(c.AgentID)
			}
		}
	}
	return ids
}

// shownCalls returns the call on the call card of each agent: the newest
// open call, else the newest ended one, else "".
func (m *Model) shownCalls(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if open := m.store.OpenCalls(id); len(open) > 0 {
			out[id] = open[0].CallID
		} else if ended := m.store.RecentEnded(id, 1); len(ended) > 0 {
			out[id] = ended[0].CallID
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

// setState applies the collector state of a host. A state without a host is
// the local collector's and replaces every per-host state. A change writes
// a HOST line; a detail goes to the diagnostics, so the coverage panel
// shows it.
func (m *Model) setState(msg feed.StateMsg) {
	host, state, detail := text.Clean(msg.Host), text.Clean(msg.State), text.Clean(msg.Detail)
	prev := m.feedState
	if host == "" {
		m.feedState = state
		clear(m.hostState)
	} else {
		if s, ok := m.hostState[host]; ok {
			prev = s
		}
		m.hostState[host] = state
	}
	label := host
	if label == "" {
		label = localHost
		if hosts := m.store.Hosts(); len(hosts) > 0 {
			label = hosts[0]
		}
	}
	if detail != "" {
		m.addDiag(label + " collector " + state + ": " + detail)
	}
	if state != prev && !m.ui.Paused {
		m.log.Host(m.nowMs(), label, state)
	}
}

// segments returns the status bar segment of each host: the store's hosts,
// then hosts known only from a StateMsg, else the local collector.
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
			st = m.feedState
		}
		out = append(out, HostStatus{Label: l, State: st, Degraded: m.degraded(l)})
	}
	return out
}
