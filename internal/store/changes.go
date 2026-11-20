package store

import (
	"cmp"
	"slices"

	"agent-view/internal/lifecycle"
	"agent-view/internal/schema"
)

// historyMs: an event whose skew-corrected time is more than this older than its arrival is history.
const historyMs = 10_000

// ChangeKind names one effect of an applied envelope.
type ChangeKind uint8

// The kinds of change.
const (
	CallStarted ChangeKind = iota
	CallEnded
	AgentAdded
	AgentStatus
	CallOutput
)

// Change is one effect of an applied envelope. AtMs is the skew-corrected time
// of the envelope; for a snapshot, it is the time of the item (the agent's last
// event, the call's start); for a call timeout (tool.end with ok null), it is
// the call's last activity, which is also its EndMs. History is true when AtMs is more than 10 s older
// than the arrival, which is Options.Now when Apply runs. Status is set for
// AgentStatus only.
type Change struct {
	Kind                          ChangeKind
	Host, AgentID, CallID, Status string
	AtMs                          int64
	History                       bool
}

// Counters are an agent's totals. They are kept apart from the call list, so
// trimming calls does not lower them.
type Counters struct {
	Calls       int   // distinct calls seen
	Faults      int   // calls that ended with ok false
	FirstSeenMs int64 // the oldest record time of the agent
}

// Touch is an unattributed file change released by TakeUnattributed.
type Touch struct {
	Host, Path string
	AtMs       int64
}

// emitter collects the changes of one envelope.
type emitter struct {
	host    string
	arrival int64
	out     []Change
}

func (em *emitter) add(kind ChangeKind, agentID, callID, status string, atMs int64) {
	em.out = append(em.out, Change{
		Kind: kind, Host: em.host, AgentID: agentID, CallID: callID, Status: status,
		AtMs: atMs, History: em.arrival-atMs > historyMs,
	})
}

// callMark is what a tally remembers of one call. The zero value is an unknown call.
type callMark uint8

const (
	callUnknown callMark = iota
	callOpen             // open and in the call list
	callEnded            // ended; trimming may have dropped it from the call list
	callDropped          // open when a snapshot or the sweep dropped it from the call list
)

// tally holds an agent's counters and every call it made. It travels with the
// agent into and out of the dormant map.
type tally struct {
	calls       map[string]callMark
	faults      int
	firstSeenMs int64
}

func newTally(firstSeenMs int64) *tally {
	return &tally{calls: map[string]callMark{}, firstSeenMs: firstSeenMs}
}

// seen moves the first-seen time back to ms when ms is older.
func (t *tally) seen(ms int64) { t.firstSeenMs = min(t.firstSeenMs, ms) }

// tallyOf returns the tally of a stored or dormant agent, or nil.
func tallyOf(h *hostState, id string) *tally {
	if a, ok := h.agents.Get(id); ok {
		return a.tally
	}
	if d, ok := h.dormant.Get(id); ok {
		return d.tally
	}
	return nil
}

func markOf(h *hostState, agentID, callID string) callMark {
	if t := tallyOf(h, agentID); t != nil {
		return t.calls[callID]
	}
	return callUnknown
}

// replay reports whether e repeats a tool.start or tool.end that the store
// applied. A replay emits no change and counts nothing. It only restores a call
// that the call list lost, with what the event carries: a dropped open call
// comes back open, and an ended call waits in pending for its replayed
// tool.end. A dormant agent stays dormant.
func (s *Store) replay(h *hostState, e schema.Envelope, at int64) bool {
	switch e.Kind {
	case "tool.start":
		c := e.Call
		if c == nil {
			return false
		}
		t := tallyOf(h, c.AgentID)
		if t == nil || t.calls[c.CallID] == callUnknown {
			return false
		}
		k := callKey{c.AgentID, c.CallID}
		if !h.agents.Has(c.AgentID) || h.calls.has(k) {
			return true
		}
		switch t.calls[c.CallID] {
		case callDropped:
			addCall(h, e.Host, *c, s.ms(h, c.StartedAt))
			t.calls[c.CallID] = callOpen
		case callEnded:
			h.pending[k] = newCall(e.Host, *c, s.ms(h, c.StartedAt))
		}
		return true
	case "tool.end":
		if markOf(h, e.AgentID, e.CallID) != callEnded {
			return false
		}
		k := callKey{e.AgentID, e.CallID}
		if call, ok := h.pending[k]; ok {
			a, stored := h.agents.Get(e.AgentID)
			if !stored {
				return true
			}
			delete(h.pending, k)
			endCall(call, e, endMs(a, call, e, at))
			storeCall(h, call)
			trimCalls(h)
		}
		return true
	}
	return false
}

// dropOpen marks an open call that left the call list. A replayed tool.start
// restores it.
func dropOpen(h *hostState, c *ViewCall) {
	if t := tallyOf(h, c.AgentID); t != nil && !c.Ended && t.calls[c.CallID] == callOpen {
		t.calls[c.CallID] = callDropped
	}
}

// earliest returns the older of at and a skew-corrected ISO time. An invalid time is ignored.
func earliest(h *hostState, iso string, at int64) int64 {
	if t, ok := lifecycle.ParseISO(iso); ok {
		return min(at, t+h.offset)
	}
	return at
}

// holder returns the first host that stores an agent, and the agent.
func (s *Store) holder(id string) (*hostState, *agentEntry) {
	for _, label := range s.hostOrder {
		h := s.hosts[label]
		if a, ok := h.agents.Get(id); ok {
			return h, a
		}
	}
	return nil, nil
}

// Counters returns the counters of a stored agent, from the first host that has
// it. A dormant or unknown agent has zero counters.
func (s *Store) Counters(agentID string) Counters {
	_, a := s.holder(agentID)
	if a == nil {
		return Counters{}
	}
	return Counters{Calls: len(a.tally.calls), Faults: a.tally.faults, FirstSeenMs: a.tally.firstSeenMs}
}

// OpenCalls returns the open calls of a stored agent, newest StartMs first; on
// equal starts, the last stored first. Do not change the calls.
func (s *Store) OpenCalls(agentID string) []*ViewCall {
	h, _ := s.holder(agentID)
	if h == nil {
		return nil
	}
	return slices.Clone(h.calls.open[agentID])
}

// RecentEnded returns at most n ended calls of a stored agent, newest EndMs
// first; on equal ends, the last stored first. Do not change the calls.
func (s *Store) RecentEnded(agentID string, n int) []*ViewCall {
	h, _ := s.holder(agentID)
	if h == nil || n <= 0 {
		return nil
	}
	ended := h.calls.ended[agentID]
	return slices.Clone(ended[:min(n, len(ended))])
}

// newestFirst orders calls by a time, newest first; on equal times, the last stored first.
func newestFirst(at func(*ViewCall) int64) func(a, b *ViewCall) int {
	return func(a, b *ViewCall) int {
		return cmp.Or(cmp.Compare(at(b), at(a)), cmp.Compare(b.seq, a.seq))
	}
}

// HostStats returns the number of agents a host has loaded, dormant agents
// included, and the number of distinct calls they made.
func (s *Store) HostStats(host string) (agents, calls int) {
	h := s.hosts[host]
	if h == nil {
		return 0, 0
	}
	for _, a := range h.agents.All() {
		calls += len(a.tally.calls)
	}
	for _, d := range h.dormant.All() {
		calls += len(d.tally.calls)
	}
	return h.agents.Len() + h.dormant.Len(), calls
}

// TakeUnattributed releases the held unattributed touches, hosts in order of
// first appearance. A touch is held for 2 s. It is dropped when a confirmed
// write of the same path is at most 2 s before or after it, or when it is as
// old as the file lifetime: then it is history, not live activity. Write times
// older than 4 s are forgotten.
func (s *Store) TakeUnattributed(now int64) []Touch {
	var out []Touch
	ttl := s.lc.FileTTLMs
	for _, label := range s.hostOrder {
		h := s.hosts[label]
		if len(h.held) > 0 {
			var keep []heldTouch
			for _, t := range h.held {
				switch {
				case now-t.atMs >= ttl:
				case now-t.atMs < flickerMs:
					keep = append(keep, t)
				case !nearWrite(h, t):
					out = append(out, Touch{Host: label, Path: t.path, AtMs: t.atMs})
				}
			}
			h.held = keep
		}
		// A held touch needs write times back to atMs - 2 s, and atMs > now - 2 s while it is held.
		for path, times := range h.recent.All() {
			live := slices.DeleteFunc(times, func(t int64) bool { return t < now-2*flickerMs })
			if len(live) == 0 {
				h.recent.Delete(path)
			} else if len(live) != len(times) {
				h.recent.Set(path, live)
			}
		}
	}
	return out
}

func nearWrite(h *hostState, t heldTouch) bool {
	times, _ := h.recent.Get(t.path)
	return slices.ContainsFunc(times, func(w int64) bool { return abs(w-t.atMs) <= flickerMs })
}
