// Package store holds the viewer state built from collector envelopes: agents,
// calls, presence, and lane order. It is a port of viewer/store.ts. Times are
// Unix milliseconds.
package store

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"time"

	"agent-view/internal/schema"
)

// Limits from viewer/store.ts, plus the dormant cap.
const (
	outputKeep       = 16 * 1024 // bytes of output kept per call
	maxCallsPerHost  = 2000
	maxRecentPerPath = 16
	maxRecentPaths   = 4000
	maxHeld          = 10_000
	sweepEveryMs     = 1000
	flickerMs        = 2000 // a grey flicker is held this long, and a confirmed write this close to it suppresses it
	// An agent is dropped after this many lingers without an event, unless a present agent descends from it.
	dropAfterLingers = 2
	maxDormant       = 10_000
)

// Options configures a Store. A zero or negative duration takes the lifecycle default.
type Options struct {
	Now            func() int64 // current Unix ms; nil uses the wall clock
	LingerMs       int64
	StaleMs        int64
	FinishedFadeMs int64
	FileTTLMs      int64
}

// ViewAgent is a stored agent. LastEventMs is skew-corrected, and LastEventAt holds the same time.
type ViewAgent struct {
	schema.Agent
	Host        string
	LastEventMs int64
	Stale       bool
}

// ViewCall is a stored call. EndMs is valid only when Ended.
type ViewCall struct {
	schema.Call
	Host            string
	StartMs, EndMs  int64
	Ended           bool
	Ok              *bool
	ExitCode        *int
	Output          string
	OutputTruncated bool
	seq             uint64 // order of storing within the host, for ties
}

// Store applies envelopes and answers the board's queries. It is not safe for concurrent use.
type Store struct {
	// UnknownEvents counts events dropped because their agent was never seen on that host.
	UnknownEvents int
	// Panics counts envelopes dropped because applying them panicked.
	Panics int

	now          func() int64
	lc           lifecycle
	hosts        map[string]*hostState
	hostOrder    []string // first appearance
	orderCounter int
	// version is bumped by every Apply and by a sweep that drops agents. A cached presence is valid for one version.
	version     uint64
	swept       bool
	lastSweepMs int64
	// presenceComputes counts presence computations. Repeated reads with one now must not add to it.
	presenceComputes int
}

type agentEntry struct {
	agent       schema.Agent
	lastEventMs int64
	tally       *tally
}

// dormantRecord is what the store keeps of an agent that the sweep or a snapshot dropped.
type dormantRecord struct {
	agent       schema.Agent
	rank        int
	lastEventMs int64
	tally       *tally
}

type heldTouch struct {
	path string
	atMs int64
}

type callKey struct{ agentID, callID string }

type hostState struct {
	offset   int64 // our clock minus the host's clock at its hello
	demo     bool
	coverage *schema.Coverage
	agents   *omap[string, *agentEntry]
	// order is the first-appearance rank of every agent seen on this host, except
	// dormant agents, whose record holds it. It survives snapshots.
	order       map[string]int
	dormant     *omap[string, *dormantRecord]
	calls       *omap[callKey, *ViewCall]
	openByAgent map[string]map[string]*ViewCall
	pending     map[callKey]*ViewCall  // calls restored from a replayed tool.start, waiting for their replayed tool.end
	nextSeq     uint64                 // seq of the next stored call
	held        []heldTouch            // unattributed touches
	recent      *omap[string, []int64] // recent confirmed write times per path, for suppressing grey flickers
	presence    *presence              // present agents for one (version, now)
}

type presence struct {
	version uint64
	now     int64
	agents  []agentEntry
}

// New returns an empty store.
func New(o Options) *Store {
	now := o.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Store{now: now, lc: resolve(o), hosts: map[string]*hostState{}}
}

// Apply applies one envelope and returns its changes, or nil. Envelopes with
// another version, no host, or no kind are ignored, as are malformed events. A
// replayed tool.start or tool.end emits and counts nothing. A panic drops the
// envelope and counts it in Panics; changes made before the panic stay.
func (s *Store) Apply(e schema.Envelope) (changes []Change) {
	if e.V != schema.Version || e.Host == "" || e.Kind == "" {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			s.Panics++
			changes = nil
		}
	}()
	em := &emitter{host: e.Host, arrival: s.now()}
	s.version++
	s.dispatch(e, em)
	return em.out
}

// Hosts returns every host label in order of first appearance.
func (s *Store) Hosts() []string { return slices.Clone(s.hostOrder) }

// Coverage returns the last coverage a host sent.
func (s *Store) Coverage(host string) (schema.Coverage, bool) {
	h := s.hosts[host]
	if h == nil || h.coverage == nil {
		return schema.Coverage{}, false
	}
	return *h.coverage, true
}

// IsDemo reports whether the last hello of any host was a demo.
func (s *Store) IsDemo() bool {
	for _, h := range s.hosts {
		if h.demo {
			return true
		}
	}
	return false
}

// Agents returns the present agents, hosts in first-appearance order.
func (s *Store) Agents(now int64) []ViewAgent {
	s.sweep(now)
	var out []ViewAgent
	for _, label := range s.hostOrder {
		for _, a := range s.presentAgents(s.hosts[label], now) {
			out = append(out, ViewAgent{Agent: a.agent, Host: label, LastEventMs: a.lastEventMs, Stale: isStale(a.agent, now, s.lc)})
		}
	}
	return out
}

// Agent returns a stored agent, present or not, from the first host that has it.
// Stale is computed at Options.Now. A dormant agent is not returned.
func (s *Store) Agent(id string) (ViewAgent, bool) {
	for _, label := range s.hostOrder {
		if a, ok := s.hosts[label].agents.get(id); ok {
			v := ViewAgent{Agent: a.agent, Host: label, LastEventMs: a.lastEventMs}
			v.LastEventAt = isoOf(a.lastEventMs)
			v.Stale = isStale(v.Agent, s.now(), s.lc)
			return v, true
		}
	}
	return ViewAgent{}, false
}

// LaneOrder returns the present agent IDs: hosts by first appearance; main
// agents, each followed by its sub-agents depth first.
func (s *Store) LaneOrder(now int64) []string {
	s.sweep(now)
	var out []string
	for _, label := range s.hostOrder {
		h := s.hosts[label]
		present := slices.Clone(s.presentAgents(h, now))
		slices.SortStableFunc(present, func(a, b agentEntry) int { return cmp.Compare(rank(h, a.agent.ID), rank(h, b.agent.ID)) })
		ids := idSet(present)
		children := map[string][]string{}
		var mains []string
		for _, p := range present {
			a := p.agent
			if a.ParentID != nil && ids[*a.ParentID] && *a.ParentID != a.ID {
				children[*a.ParentID] = append(children[*a.ParentID], a.ID)
			} else {
				mains = append(mains, a.ID)
			}
		}
		seen := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			out = append(out, id)
			for _, k := range children[id] {
				walk(k)
			}
		}
		for _, m := range mains {
			walk(m)
		}
		// Parent cycles never reach a main agent. Keep them visible.
		for _, p := range present {
			walk(p.agent.ID)
		}
	}
	return out
}

// ---- internals -------------------------------------------------------

func (s *Store) host(label string) *hostState {
	h := s.hosts[label]
	if h == nil {
		h = &hostState{
			agents: newOmap[string, *agentEntry](), order: map[string]int{}, dormant: newOmap[string, *dormantRecord](),
			calls: newOmap[callKey, *ViewCall](), openByAgent: map[string]map[string]*ViewCall{}, pending: map[callKey]*ViewCall{},
			recent: newOmap[string, []int64](),
		}
		s.hosts[label] = h
		s.hostOrder = append(s.hostOrder, label)
	}
	return h
}

// presentAgents returns the present agents of one host, with skew-corrected
// LastEventAt. It is cached per (version, now). Do not change the result.
func (s *Store) presentAgents(h *hostState, now int64) []agentEntry {
	if p := h.presence; p != nil && p.version == s.version && p.now == now {
		return p.agents
	}
	s.presenceComputes++
	adjusted := make([]agentEntry, 0, h.agents.len())
	plain := make([]schema.Agent, 0, h.agents.len())
	for _, a := range h.agents.all() {
		ag := a.agent
		ag.LastEventAt = isoOf(a.lastEventMs)
		adjusted = append(adjusted, agentEntry{agent: ag, lastEventMs: a.lastEventMs})
		plain = append(plain, ag)
	}
	present := presentAgentIDs(plain, now, s.lc)
	agents := adjusted[:0]
	for _, a := range adjusted {
		if present[a.agent.ID] {
			agents = append(agents, a)
		}
	}
	h.presence = &presence{version: s.version, now: now, agents: agents}
	return agents
}

// sweep moves agents far past linger into the dormant map and drops their
// calls. It runs at most once per second of now.
func (s *Store) sweep(now int64) {
	if s.swept && abs(now-s.lastSweepMs) < sweepEveryMs {
		return
	}
	s.swept, s.lastSweepMs = true, now
	horizon := dropAfterLingers * s.lc.lingerMs
	anyDropped := false
	for _, label := range s.hostOrder {
		h := s.hosts[label]
		var present map[string]bool
		dropped := false
		for id, a := range h.agents.all() {
			if now-a.lastEventMs < horizon {
				continue
			}
			if present == nil {
				present = idSet(s.presentAgents(h, now))
			}
			if present[id] {
				continue // an ancestor of a present agent
			}
			toDormant(h, id, a)
			dropped = true
		}
		if !dropped {
			continue
		}
		anyDropped = true
		for k, c := range h.calls.all() {
			if !h.agents.has(c.AgentID) {
				h.calls.delete(k)
				dropOpen(h, c)
			}
		}
		maps.DeleteFunc(h.pending, func(_ callKey, c *ViewCall) bool { return !h.agents.has(c.AgentID) })
	}
	if anyDropped {
		s.version++
	}
}

// toDormant moves a stored agent's record into the dormant map. The oldest record goes past the cap.
func toDormant(h *hostState, id string, a *agentEntry) {
	h.agents.delete(id)
	h.dormant.set(id, &dormantRecord{agent: a.agent, rank: rank(h, id), lastEventMs: a.lastEventMs, tally: a.tally})
	if h.dormant.len() > maxDormant {
		oldest, _, _ := h.dormant.first()
		h.dormant.delete(oldest)
	}
	delete(h.order, id)
	delete(h.openByAgent, id)
}

// wake removes a dormant record and gives the agent its rank back.
func wake(h *hostState, id string) (*dormantRecord, bool) {
	d, ok := h.dormant.get(id)
	if !ok {
		return nil, false
	}
	h.dormant.delete(id)
	h.order[id] = d.rank
	return d, true
}

// revive brings a dormant agent back with its record, and its dormant
// ancestors with theirs, as the TypeScript resnapshot did. It reports false
// when the agent is not dormant.
func revive(h *hostState, id string) bool {
	d, ok := wake(h, id)
	if !ok {
		return false
	}
	h.agents.set(id, &agentEntry{agent: d.agent, lastEventMs: d.lastEventMs, tally: d.tally})
	// Each wake removes a dormant record, so a parent cycle ends the walk.
	for d.agent.ParentID != nil && !h.agents.has(*d.agent.ParentID) {
		pid := *d.agent.ParentID
		if d, ok = wake(h, pid); !ok {
			break
		}
		h.agents.set(pid, &agentEntry{agent: d.agent, lastEventMs: d.lastEventMs, tally: d.tally})
	}
	return true
}

// ms returns a skew-corrected event time. A missing or invalid time is now.
func (s *Store) ms(h *hostState, iso string) int64 {
	t, ok := parseISO(iso)
	if !ok {
		return s.now()
	}
	return t + h.offset
}

// agentMs returns a skew-corrected agent time. A missing or invalid time is the epoch.
func agentMs(h *hostState, iso string) int64 {
	t, ok := parseISO(iso)
	if !ok {
		return 0
	}
	return t + h.offset
}

func (s *Store) note(h *hostState, id string) {
	if _, ok := h.order[id]; !ok {
		h.order[id] = s.orderCounter
		s.orderCounter++
	}
}

func bump(h *hostState, agentID string, atMs int64) {
	if a, ok := h.agents.get(agentID); ok {
		a.lastEventMs = max(a.lastEventMs, atMs)
	}
}

func rank(h *hostState, id string) int {
	if r, ok := h.order[id]; ok {
		return r
	}
	return math.MaxInt
}

func idSet(agents []agentEntry) map[string]bool {
	set := make(map[string]bool, len(agents))
	for _, a := range agents {
		set[a.agent.ID] = true
	}
	return set
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
