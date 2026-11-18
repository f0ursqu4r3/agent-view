package store

import (
	"slices"
	"unicode/utf8"

	"agent-view/internal/schema"
)

func (s *Store) dispatch(e schema.Envelope, em *emitter) {
	h := s.host(e.Host)
	at := s.ms(h, e.Ts)
	// No double counting: a replayed tool.start or tool.end changes nothing, not even a dormant agent.
	if replayed(h, e) {
		return
	}
	// The collector keeps every agent. An event for a dormant agent revives it.
	// An event for an agent never seen is dropped: the feed has no resnapshot path.
	if id := subjectOf(e); id != "" {
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
		if a, ok := h.agents.get(e.AgentID); ok && a.agent.Status != e.Status {
			a.agent.Status = e.Status
			em.add(AgentStatus, e.AgentID, "", e.Status, at)
		}
		bump(h, e.AgentID, at)
	case "tool.start":
		s.toolStart(h, e.Host, e.Call, at, em)
	case "tool.update":
		call, _ := h.calls.get(callKey{e.AgentID, e.CallID})
		bump(h, e.AgentID, at)
		files := e.UpdateFiles()
		if files == nil || call == nil || call.Ended {
			return
		}
		call.Files = union(call.Files, files)
	case "tool.output":
		call, _ := h.calls.get(callKey{e.AgentID, e.CallID})
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
		if e.Chunk != "" || truncated != call.OutputTruncated {
			em.add(CallOutput, e.AgentID, e.CallID, "", at)
		}
		call.Output, call.OutputTruncated = out, truncated
	case "tool.end":
		bump(h, e.AgentID, at)
		if a, ok := h.agents.get(e.AgentID); ok {
			if m := a.tally.calls[e.CallID]; m == callOpen || m == callDropped {
				a.tally.calls[e.CallID] = callEnded
				if e.Ok != nil && !*e.Ok {
					a.tally.faults++
				}
			}
		}
		if call, ok := h.calls.get(callKey{e.AgentID, e.CallID}); ok && !call.Ended {
			call.Ended, call.EndMs = true, at
			call.Ok = clonePtr(e.Ok)
			if e.ExitCode != nil {
				call.ExitCode = clonePtr(e.ExitCode)
			}
			closeOpen(h, call)
			em.add(CallEnded, e.AgentID, e.CallID, "", at)
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
// become dormant with their records, and agent ranks survive. It reports an
// agent new to the store and an open call shown anew; a call the store saw end
// is not opened again.
func (s *Store) snapshot(h *hostState, e schema.Envelope, at int64, em *emitter) {
	files := e.SnapshotFiles()
	if e.Agents == nil || e.OpenCalls == nil || files == nil {
		return
	}
	oldAgents, oldCalls := h.agents, h.calls
	h.agents = newOmap[string, *agentEntry]()
	h.calls = newOmap[callKey, *ViewCall]()
	h.openByAgent = map[string]map[string]*ViewCall{}
	h.held = nil
	h.recent = newOmap[string, []int64]()
	for _, a := range e.Agents {
		if a.ID == "" {
			continue
		}
		var t *tally
		if cur, ok := h.agents.get(a.ID); ok { // listed twice
			t = cur.tally
		} else if prev, ok := oldAgents.get(a.ID); ok {
			t = prev.tally
		} else if d, ok := wake(h, a.ID); ok { // a dormant agent keeps its rank and record
			t = d.tally
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
		h.agents.set(a.ID, &agentEntry{agent: a, lastEventMs: last, tally: t})
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
		if mark != callOpen { // callOpen: it was open in the call list before this snapshot
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

// toolStart opens a new call, or shows a dropped call again without counting it
// again. Its files are not writes, so they record nothing.
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
	if a.agent.Status != "working" {
		a.agent.Status = "working"
		em.add(AgentStatus, c.AgentID, "", "working", at)
	}
	bump(h, c.AgentID, at)
	trimCalls(h)
}

func addCall(h *hostState, host string, c schema.Call, startMs int64) *ViewCall {
	c.Files = slices.Clone(c.Files)
	call := &ViewCall{Call: c, Host: host, StartMs: startMs, seq: h.nextSeq}
	h.nextSeq++
	h.calls.set(callKey{c.AgentID, c.CallID}, call)
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
