package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"agent-view/internal/schema"
)

// refOpen is the reference for OpenCalls: the open calls of the agent's host
// in the full call table, newest StartMs first, then the last stored first.
func refOpen(s *Store, key AgentKey) []*ViewCall {
	h, _ := s.stored(key)
	if h == nil {
		return nil
	}
	var out []*ViewCall
	for _, c := range h.calls.all() {
		if !c.Ended && c.AgentID == key.ID {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, newestFirst(func(c *ViewCall) int64 { return c.StartMs }))
	return out
}

// refEnded is the reference for RecentEnded: the algorithm before the index,
// which sorts every ended call of the agent's host.
func refEnded(s *Store, key AgentKey, n int) []*ViewCall {
	h, _ := s.stored(key)
	if h == nil || n <= 0 {
		return nil
	}
	var out []*ViewCall
	for _, c := range h.calls.all() {
		if c.Ended && c.AgentID == key.ID {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, newestFirst(func(c *ViewCall) int64 { return c.EndMs }))
	return out[:min(n, len(out))]
}

// checkIndex fails when a host's per-agent lists differ from its call table:
// every call in the table is in exactly one list of its agent, every listed
// call is in the table, the lists are in order, and no list is empty.
func checkIndex(t *testing.T, s *Store, step int) {
	t.Helper()
	for _, label := range s.hostOrder {
		h := s.hosts[label]
		want := map[*ViewCall]bool{}
		for k, c := range h.calls.all() {
			if k != (callKey{c.AgentID, c.CallID}) {
				t.Fatalf("step %d, host %s: call %v stored under key %v", step, label, c.CallID, k)
			}
			want[c] = true
		}
		seen := 0
		for kind, lists := range map[string]map[string][]*ViewCall{"open": h.calls.open, "ended": h.calls.ended} {
			for agentID, list := range lists {
				if len(list) == 0 {
					t.Fatalf("step %d, host %s: empty %s list kept for %s", step, label, kind, agentID)
				}
				at := func(c *ViewCall) int64 { return c.StartMs }
				if kind == "ended" {
					at = func(c *ViewCall) int64 { return c.EndMs }
				}
				if !slices.IsSortedFunc(list, newestFirst(at)) {
					t.Fatalf("step %d, host %s: %s list of %s out of order", step, label, kind, agentID)
				}
				for _, c := range list {
					if !want[c] || c.AgentID != agentID || c.Ended != (kind == "ended") {
						t.Fatalf("step %d, host %s: %s list of %s holds %s/%s (ended %v, stored %v)",
							step, label, kind, agentID, c.AgentID, c.CallID, c.Ended, want[c])
					}
					seen++
				}
			}
		}
		if seen != len(want) {
			t.Fatalf("step %d, host %s: lists hold %d calls, table %d", step, label, seen, len(want))
		}
	}
}

func samePtrs(a, b []*ViewCall) bool { return slices.Equal(a, b) }

// TestCallIndexMatchesFullScan applies a random sequence of events to several
// hosts and agents. After every step, OpenCalls and RecentEnded of every agent
// equal the reference computed from the full call table, and the index matches
// the table. The sequence covers new calls, updates, output, ends, call
// timeouts, replays, snapshots, the call cap, the sweep, and dormant agents.
func TestCallIndexMatchesFullScan(t *testing.T) {
	const steps = 4000
	r := rand.New(rand.NewPCG(1, 2))
	clock := nowMs
	s := newStore(t, Options{Now: func() int64 { return clock }, LingerMs: 60_000, FinishedFadeMs: 20_000})
	hosts := []string{"local", "box", "box#2"}
	// "a0" to "a2" exist on every host; the others on one host only.
	agentsOf := map[string][]string{}
	var keys []AgentKey // every agent of every host, an agent no host has seen, and an unknown host
	for i, h := range hosts {
		agentsOf[h] = []string{"a0", "a1", "a2"}
		for k := range 3 {
			agentsOf[h] = append(agentsOf[h], fmt.Sprintf("h%d-%d", i, k))
		}
		for _, id := range agentsOf[h] {
			keys = append(keys, AgentKey{h, id})
		}
		keys = append(keys, AgentKey{h, "never"})
	}
	keys = append(keys, AgentKey{"nohost", "a0"})
	type started struct{ host, agent, id string }
	var calls []started // every tool.start sent, for ends and replays
	next := 0
	// Coarse times make equal start and end times common.
	at := func() int64 { return clock - int64(r.IntN(6))*1000 }
	pickHost := func() string { return hosts[r.IntN(len(hosts))] }
	pickAgent := func(h string) string {
		if r.IntN(20) == 0 {
			return "never" // an agent no host has seen
		}
		return agentsOf[h][r.IntN(len(agentsOf[h]))]
	}
	newCall := func(h string) started {
		next++
		c := started{h, pickAgent(h), fmt.Sprintf("c%d", next)}
		calls = append(calls, c)
		return c
	}
	pickCall := func() (started, bool) {
		if len(calls) == 0 {
			return started{}, false
		}
		// Mostly recent calls, sometimes any.
		if r.IntN(4) == 0 {
			return calls[r.IntN(len(calls))], true
		}
		return calls[max(0, len(calls)-1-r.IntN(40))], true
	}
	endEv := func(c started, ts int64) schema.Envelope {
		var ok *bool
		switch r.IntN(4) {
		case 0:
			ok = nil // a call timeout
		case 1:
			ok = ptr(false)
		default:
			ok = ptr(true)
		}
		return evEnd(c.host, c.agent, c.id, ok, nil, nil, ts)
	}

	for _, h := range hosts {
		s.Apply(evHello(h, clock, false))
		for _, id := range agentsOf[h] {
			s.Apply(evUpsert(h, agent(id, withLastAt(clock)), clock))
		}
	}
	trimmed := false   // a host reached the call cap
	endedSeen := false // an agent had ended calls
	for step := range steps {
		switch op := r.IntN(100); {
		case op < 25: // a new call
			c := newCall(pickHost())
			ts := at()
			s.Apply(evStart(c.host, call(c.agent, c.id, withStart(ts)), ts))
		case op < 30: // a replayed or repeated tool.start
			if c, ok := pickCall(); ok {
				ts := at()
				s.Apply(evStart(c.host, call(c.agent, c.id, withStart(ts)), ts))
			}
		case op < 35:
			if c, ok := pickCall(); ok {
				s.Apply(evUpdate(c.host, c.agent, c.id, []string{fmt.Sprintf("/p/f%d", r.IntN(5))}, at()))
			}
		case op < 40:
			if c, ok := pickCall(); ok {
				s.Apply(evOutput(c.host, c.agent, c.id, "x", false, at()))
			}
		case op < 62: // an end, a replayed end, or a call timeout
			if c, ok := pickCall(); ok {
				s.Apply(endEv(c, at()))
			}
		case op < 65:
			h := pickHost()
			s.Apply(evStatus(h, pickAgent(h), []string{"working", "idle", "finished"}[r.IntN(3)], at()))
		case op < 69: // an upsert revives a dormant agent
			h := pickHost()
			id := agentsOf[h][r.IntN(len(agentsOf[h]))]
			s.Apply(evUpsert(h, agent(id, withLastAt(clock)), clock))
		case op < 73: // a snapshot of some agents and open calls, some of unlisted agents
			h := pickHost()
			var as []schema.Agent
			for _, id := range agentsOf[h] {
				if r.IntN(3) > 0 {
					as = append(as, agent(id, withLastAt(clock-int64(r.IntN(5))*1000)))
				}
			}
			var open []schema.Call
			for range r.IntN(6) {
				c := newCall(h)
				if r.IntN(2) == 0 {
					c, _ = pickCall() // a call the store knows, maybe of another host
					c.host = h
				}
				ts := at()
				open = append(open, call(c.agent, c.id, withStart(ts)))
				if r.IntN(8) == 0 {
					open = append(open, call(c.agent, c.id, withStart(ts+1000))) // listed twice
				}
			}
			s.Apply(evSnapshot(h, as, open))
		case op < 74: // an empty snapshot replaces the touches only
			s.Apply(evSnapshot(pickHost(), nil, nil))
		case op < 77: // a burst of calls drives the host past the call cap
			h := pickHost()
			for range 1500 + r.IntN(1200) {
				c := newCall(h)
				ts := at()
				s.Apply(evStart(c.host, call(c.agent, c.id, withStart(ts)), ts))
				s.Apply(endEv(c, at()))
			}
		case op < 80: // the sweep makes idle agents dormant and drops their calls
			clock += int64(r.IntN(3)) * 70_000
			s.Agents(clock)
		case op < 81:
			s.Apply(evHello(pickHost(), clock, false))
		default:
			clock += int64(r.IntN(3)) * 500
			s.Agents(clock)
		}
		for _, h := range s.hosts {
			trimmed = trimmed || h.calls.len() >= maxCallsPerHost
		}
		checkIndex(t, s, step)
		for _, key := range keys {
			if got, want := s.OpenCalls(key), refOpen(s, key); !samePtrs(got, want) {
				t.Fatalf("step %d: OpenCalls(%v) = %v, want %v", step, key, callIDs(got), callIDs(want))
			}
			for _, n := range []int{0, 1, 3, 1_000_000} {
				got, want := s.RecentEnded(key, n), refEnded(s, key, n)
				if !samePtrs(got, want) {
					t.Fatalf("step %d: RecentEnded(%v, %d) = %v, want %v", step, key, n, callIDs(got), callIDs(want))
				}
				endedSeen = endedSeen || len(got) > 0
			}
		}
	}
	// The sequence must reach the paths it claims to cover.
	dormant := false
	for _, h := range s.hosts {
		dormant = dormant || h.dormant.Len() > 0
	}
	if !trimmed || !dormant || !endedSeen {
		t.Fatalf("sequence too weak: trimmed %v, dormant %v, ended calls seen %v", trimmed, dormant, endedSeen)
	}
}

// TestRecentEndedReturnsACopy: a caller that appends to the result does not change the store.
func TestRecentEndedReturnsACopy(t *testing.T) {
	s := seeded(t)
	for i, id := range []string{"c1", "c2", "c3"} {
		at := nowMs - int64(10-i)*1000
		s.Apply(evStart("local", call("a", id, withStart(at)), at))
		s.Apply(evEnd("local", "a", id, ptr(true), nil, nil, at))
	}
	got := s.RecentEnded(k("a"), 1)
	_ = append(got, &ViewCall{})
	eq(t, "recent", callIDs(s.RecentEnded(k("a"), 3)), []string{"c3", "c2", "c1"})
	open := s.OpenCalls(k("a"))
	_ = append(open, &ViewCall{})
	eq(t, "open", callIDs(s.OpenCalls(k("a"))), []string{})
}
