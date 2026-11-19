package store

import (
	"slices"
	"unicode/utf8"

	"agent-view/internal/schema"
)

func (s *Store) dispatch(e schema.Envelope, em *emitter) {
	h := s.host(e.Host)
	at := s.ms(h, e.Ts)
	// No double counting: a replayed tool.start or tool.end emits and counts nothing.
	if s.replay(h, e, at) {
		return
	}
	// The collector keeps every agent. An event for a dormant agent revives it,
	// except a call timeout, which only ends the call. An event for an agent
	// never seen is dropped: the feed has no resnapshot path.
	if id := subjectOf(e); id != "" {
		if d, ok := h.dormant.get(id); ok && isTimeout(e) {
			endTally(d.tally, e)
			return
		}
		if !h.agents.has(id) && !revive(h, id) {
			s.UnknownEvents++
			return
		}
		a, _ := h.agents.get(id)
		a.tally.seen(at)
	}
	switch e.Kind {
	case "hello":
		h.offset = 0
		if t, ok := parseISO(e.Now); ok {
			h.offset = s.now() - t
		}
		h.demo = e.Demo
	case "snapshot":
		s.snapshot(h, e, at, em)
	case "agent.upsert":
		in := e.Agent
		if in == nil || in.ID == "" {
			return
		}
		revive(h, in.ID)
		seen := max(agentMs(h, in.LastEventAt), at)
		first := earliest(h, in.LastEventAt, at)
		s.note(h, in.ID)
		if cur, ok := h.agents.get(in.ID); ok {
			// Like the collector: an upsert never overrides the tracked status.
			status, last := cur.agent.Status, cur.agent.LastEventAt
			cur.agent = *in
			cur.agent.Status, cur.agent.LastEventAt = status, last
			cur.lastEventMs = max(cur.lastEventMs, seen)
			cur.tally.seen(first)
		} else {
			h.agents.set(in.ID, &agentEntry{agent: *in, lastEventMs: seen, tally: newTally(first)})
			em.add(AgentAdded, in.ID, "", "", at)
		}
	case "agent.status":
		if a, ok := h.agents.get(e.AgentID); ok {
			setStatus(a, e.AgentID, e.Status, at, em)
		}
		bump(h, e.AgentID, at)
	case "tool.start":
		s.toolStart(h, e.Host, e.Call, at, em)
	case "tool.update":
		call := callFor(h, callKey{e.AgentID, e.CallID})
		bump(h, e.AgentID, at)
		files := e.UpdateFiles()
		if files == nil || call == nil || call.Ended {
			return
		}
		call.Files = union(call.Files, files)
	case "tool.output":
		call, stored := h.calls.get(callKey{e.AgentID, e.CallID})
		if !stored {
			call = h.pending[callKey{e.AgentID, e.CallID}] // a restored call fills in silently
		}
		bump(h, e.AgentID, at)
		if call == nil {
			return
		}
		out := call.Output + e.Chunk
		truncated := call.OutputTruncated || e.Truncated
		if len(out) > outputKeep {
			out = tail(out, outputKeep)
			truncated = true
		}
		if stored && (e.Chunk != "" || truncated != call.OutputTruncated) {
			em.add(CallOutput, e.AgentID, e.CallID, "", at)
		}
		call.Output, call.OutputTruncated = out, truncated
	case "tool.end":
		// A call timeout is no activity of the agent: it bumps nothing, and
		// the call ends at its last activity.
		if a, ok := h.agents.get(e.AgentID); ok {
			if !isTimeout(e) {
				bump(h, e.AgentID, at)
			}
			endTally(a.tally, e)
			if call, ok := h.calls.get(callKey{e.AgentID, e.CallID}); ok && !call.Ended {
				end := endMs(a, call, e, at)
				endCall(call, e, end)
				closeOpen(h, call)
				em.add(CallEnded, e.AgentID, e.CallID, "", end)
			}
		}
		for _, w := range e.Writes {
			if w.Path != "" {
				recordWrite(h, w.Path, at)
			}
		}
	case "fs.change":
		if e.Path == "" {
			return
		}
		h.held = append(h.held, heldTouch{path: e.Path, atMs: at})
		if len(h.held) > maxHeld {
			h.held = h.held[len(h.held)-maxHeld:]
		}
	case "coverage":
		if e.Coverage != nil {
			c := *e.Coverage
			h.coverage = &c
		}
	}
}

// snapshot replaces the host's agents, calls, and touches. Agents it leaves out
// become dormant with their records, and agent ranks survive. It reports agents
// and open calls new to the store; a call the store saw end is not opened again.
func (s *Store) snapshot(h *hostState, e schema.Envelope, at int64, em *emitter) {
	files := e.SnapshotFiles()
	if e.Agents == nil || e.OpenCalls == nil || files == nil {
		return
	}
	oldAgents, oldCalls := h.agents, h.calls
	h.agents = newOmap[string, *agentEntry]()
	h.calls = newOmap[callKey, *ViewCall]()
	h.openByAgent = map[string]map[string]*ViewCall{}
	h.pending = map[callKey]*ViewCall{}
	h.held = nil
	h.recent = newOmap[string, []int64]()
	for _, a := range e.Agents {
		if a.ID == "" {
			continue
		}
		var t *tally
		var statusMs int64
		if cur, ok := h.agents.get(a.ID); ok { // listed twice
			t, statusMs = cur.tally, cur.statusMs
		} else if prev, ok := oldAgents.get(a.ID); ok {
			t, statusMs = prev.tally, prev.statusMs
		} else if d, ok := wake(h, a.ID); ok { // a dormant agent keeps its rank and record
			t, statusMs = d.tally, d.statusMs
		}
		s.note(h, a.ID)
		first := earliest(h, a.LastEventAt, at)
		last := agentMs(h, a.LastEventAt)
		if t == nil {
			t = newTally(first)
			em.add(AgentAdded, a.ID, "", "", last)
		} else {
			t.seen(first)
		}
		h.agents.set(a.ID, &agentEntry{agent: a, lastEventMs: last, statusMs: statusMs, tally: t})
	}
	for id, a := range oldAgents.all() {
		if !h.agents.has(id) {
			toDormant(h, id, a)
		}
	}
	for _, c := range e.OpenCalls {
		if c.AgentID == "" || c.CallID == "" {
			continue
		}
		var t *tally
		mark := callUnknown
		if a, ok := h.agents.get(c.AgentID); ok {
			t, mark = a.tally, a.tally.calls[c.CallID]
		}
		if mark == callEnded {
			continue
		}
		start := s.ms(h, c.StartedAt)
		addCall(h, e.Host, c, start)
		if t == nil {
			continue // a call of an agent the snapshot does not list is kept but not counted
		}
		t.seen(start)
		t.calls[c.CallID] = callOpen
		if mark == callUnknown { // a dropped call comes back silently
			em.add(CallStarted, c.AgentID, c.CallID, "", start)
		}
	}
	for k, c := range oldCalls.all() {
		if !h.calls.has(k) {
			dropOpen(h, c)
		}
	}
	for _, f := range files {
		// A confirmed file is a touch seed, not a write, so it suppresses no grey flicker.
		if f.Path == "" || f.Attribution == "confirmed" {
			continue
		}
		h.held = append(h.held, heldTouch{path: f.Path, atMs: s.ms(h, f.LastTouchAt)})
	}
}

// toolStart opens a call new to the store. Its files are not writes, so they record nothing.
func (s *Store) toolStart(h *hostState, host string, c *schema.Call, at int64, em *emitter) {
	if c == nil || c.AgentID == "" || c.CallID == "" {
		return
	}
	a, ok := h.agents.get(c.AgentID)
	if !ok {
		return // dispatch drops events of unknown agents
	}
	call := addCall(h, host, *c, s.ms(h, c.StartedAt))
	a.tally.seen(call.StartMs)
	a.tally.calls[c.CallID] = callOpen
	em.add(CallStarted, c.AgentID, c.CallID, "", at)
	setStatus(a, c.AgentID, "working", at, em)
	bump(h, c.AgentID, at)
	trimCalls(h)
}

// newCall returns an open call that is not stored yet.
func newCall(host string, c schema.Call, startMs int64) *ViewCall {
	c.Files = slices.Clone(c.Files)
	return &ViewCall{Call: c, Host: host, StartMs: startMs}
}

// storeCall puts a call at the end of the call list.
func storeCall(h *hostState, call *ViewCall) {
	call.seq = h.nextSeq
	h.nextSeq++
	h.calls.set(callKey{call.AgentID, call.CallID}, call)
}

// callFor returns a stored call, or a restored call waiting for its replayed tool.end.
func callFor(h *hostState, k callKey) *ViewCall {
	if c, ok := h.calls.get(k); ok {
		return c
	}
	return h.pending[k]
}

// isTimeout reports whether e is a call timeout: the collector ends a call that
// has had no event for its call timeout with ok null at its own clock. No
// source sends ok null.
func isTimeout(e schema.Envelope) bool { return e.Kind == "tool.end" && e.Ok == nil }

// endMs returns the end time of a call that e ends at at. A call timeout comes
// long after the call's last activity, so the call ends at that activity: its
// start, or the agent's last event when that is later.
func endMs(a *agentEntry, call *ViewCall, e schema.Envelope, at int64) int64 {
	if isTimeout(e) {
		return max(call.StartMs, a.lastEventMs)
	}
	return at
}

// endTally marks a call of a tally ended, once, and counts a fault.
func endTally(t *tally, e schema.Envelope) {
	if m := t.calls[e.CallID]; m == callOpen || m == callDropped {
		t.calls[e.CallID] = callEnded
		if e.Ok != nil && !*e.Ok {
			t.faults++
		}
	}
}

func endCall(call *ViewCall, e schema.Envelope, at int64) {
	call.Ended, call.EndMs = true, at
	call.Ok = clonePtr(e.Ok)
	if e.ExitCode != nil {
		call.ExitCode = clonePtr(e.ExitCode)
	}
}

// setStatus sets the tracked status from an event at atMs: an agent.status, or
// the implicit working of a new tool.start. An event older than the time of the
// current status changes nothing, so scan events out of order and replays
// cannot bring back an old status. Equal times apply in arrival order.
func setStatus(a *agentEntry, id, status string, atMs int64, em *emitter) {
	if atMs < a.statusMs {
		return
	}
	a.statusMs = atMs
	if a.agent.Status != status {
		a.agent.Status = status
		em.add(AgentStatus, id, "", status, atMs)
	}
}

func addCall(h *hostState, host string, c schema.Call, startMs int64) *ViewCall {
	call := newCall(host, c, startMs)
	storeCall(h, call)
	open := h.openByAgent[c.AgentID]
	if open == nil {
		open = map[string]*ViewCall{}
		h.openByAgent[c.AgentID] = open
	}
	open[c.CallID] = call
	return call
}

func closeOpen(h *hostState, call *ViewCall) {
	open := h.openByAgent[call.AgentID]
	if open == nil {
		return
	}
	if open[call.CallID] == call {
		delete(open, call.CallID)
	}
	if len(open) == 0 {
		delete(h.openByAgent, call.AgentID)
	}
}

// trimCalls drops the oldest ended calls past the cap. Open calls are never dropped here.
func trimCalls(h *hostState) {
	excess := h.calls.len() - maxCallsPerHost
	for k, c := range h.calls.all() {
		if excess <= 0 {
			break
		}
		if c.Ended {
			h.calls.delete(k)
			excess--
		}
	}
}

// recordWrite keeps the time of a confirmed write. Only writes suppress grey flickers.
func recordWrite(h *hostState, path string, atMs int64) {
	times, ok := h.recent.get(path)
	if !ok {
		h.recent.set(path, []int64{atMs})
		if h.recent.len() > maxRecentPaths {
			oldest, _, _ := h.recent.first()
			h.recent.delete(oldest)
		}
		return
	}
	times = append(times, atMs)
	if len(times) > maxRecentPerPath {
		times = times[len(times)-maxRecentPerPath:]
	}
	h.recent.set(path, times)
}

// subjectOf returns the agent an event is about, for events that need a known agent.
func subjectOf(e schema.Envelope) string {
	switch e.Kind {
	case "tool.start":
		if e.Call != nil {
			return e.Call.AgentID
		}
	case "agent.status", "tool.update", "tool.output", "tool.end":
		return e.AgentID
	}
	return ""
}

// union returns a followed by the items of b it lacks, without duplicates, in first-seen order.
func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, f := range slices.Concat(a, b) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// tail returns at most the last n bytes of s, starting on a rune boundary.
func tail(s string, n int) string {
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
