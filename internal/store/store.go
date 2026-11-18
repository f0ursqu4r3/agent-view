// Package store holds the viewer state built from collector envelopes: agents,
// calls, presence, and lane order. It is a port of viewer/store.ts. Times are
// Unix milliseconds.
package store

import (
	"cmp"
	"container/list"
	"iter"
	"math"
	"slices"
	"time"
	"unicode/utf8"

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
}

// Store applies envelopes and answers the board's queries. It is not safe for concurrent use.
type Store struct {
	// UnknownEvents counts events dropped because their agent was never seen on that host.
	UnknownEvents int

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
}

// dormantRecord is what the sweep keeps of a dropped agent.
type dormantRecord struct {
	agent       schema.Agent
	rank        int
	lastEventMs int64
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

// Apply applies one envelope. Envelopes with another version, no host, or no
// kind are ignored, as are malformed events. Apply does not report changes yet
// and returns nil.
func (s *Store) Apply(e schema.Envelope) []Change {
	if e.V != schema.Version || e.Host == "" || e.Kind == "" {
		return nil
	}
	s.version++
	s.dispatch(e)
	return nil
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
			calls: newOmap[callKey, *ViewCall](), openByAgent: map[string]map[string]*ViewCall{}, recent: newOmap[string, []int64](),
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
			h.agents.delete(id)
			h.dormant.set(id, &dormantRecord{agent: a.agent, rank: rank(h, id), lastEventMs: a.lastEventMs})
			if h.dormant.len() > maxDormant {
				oldest, _, _ := h.dormant.first()
				h.dormant.delete(oldest)
			}
			delete(h.order, id)
			delete(h.openByAgent, id)
			dropped = true
		}
		if !dropped {
			continue
		}
		anyDropped = true
		for k, c := range h.calls.all() {
			if !h.agents.has(c.AgentID) {
				h.calls.delete(k)
			}
		}
	}
	if anyDropped {
		s.version++
	}
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

// revive brings a dormant agent back with its record. It reports false when the agent is not dormant.
func revive(h *hostState, id string) bool {
	d, ok := wake(h, id)
	if ok {
		h.agents.set(id, &agentEntry{agent: d.agent, lastEventMs: d.lastEventMs})
	}
	return ok
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

func (s *Store) dispatch(e schema.Envelope) {
	h := s.host(e.Host)
	at := s.ms(h, e.Ts)
	// The collector keeps every agent. An event for a dormant agent revives it.
	// An event for an agent never seen is dropped: the feed has no resnapshot path.
	if id := subjectOf(e); id != "" && !h.agents.has(id) && !revive(h, id) {
		s.UnknownEvents++
		return
	}
	switch e.Kind {
	case "hello":
		h.offset = 0
		if t, ok := parseISO(e.Now); ok {
			h.offset = s.now() - t
		}
		h.demo = e.Demo
	case "snapshot":
		s.snapshot(h, e)
	case "agent.upsert":
		in := e.Agent
		if in == nil || in.ID == "" {
			return
		}
		revive(h, in.ID)
		seen := max(agentMs(h, in.LastEventAt), at)
		s.note(h, in.ID)
		if cur, ok := h.agents.get(in.ID); ok {
			// Like the collector: an upsert never overrides the tracked status.
			status, last := cur.agent.Status, cur.agent.LastEventAt
			cur.agent = *in
			cur.agent.Status, cur.agent.LastEventAt = status, last
			cur.lastEventMs = max(cur.lastEventMs, seen)
		} else {
			h.agents.set(in.ID, &agentEntry{agent: *in, lastEventMs: seen})
		}
	case "agent.status":
		if a, ok := h.agents.get(e.AgentID); ok {
			a.agent.Status = e.Status
		}
		bump(h, e.AgentID, at)
	case "tool.start":
		s.toolStart(h, e.Host, e.Call, at)
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
		call.Output, call.OutputTruncated = out, truncated
	case "tool.end":
		call, _ := h.calls.get(callKey{e.AgentID, e.CallID})
		bump(h, e.AgentID, at)
		if call != nil {
			if call.Ended {
				return
			}
			call.Ended, call.EndMs = true, at
			call.Ok = clonePtr(e.Ok)
			if e.ExitCode != nil {
				call.ExitCode = clonePtr(e.ExitCode)
			}
			closeOpen(h, call)
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

// snapshot replaces the host's agents, calls, and touches. Agent ranks survive.
func (s *Store) snapshot(h *hostState, e schema.Envelope) {
	files := e.SnapshotFiles()
	if e.Agents == nil || e.OpenCalls == nil || files == nil {
		return
	}
	h.agents = newOmap[string, *agentEntry]()
	h.calls = newOmap[callKey, *ViewCall]()
	h.openByAgent = map[string]map[string]*ViewCall{}
	h.held = nil
	h.recent = newOmap[string, []int64]()
	for _, a := range e.Agents {
		if a.ID == "" {
			continue
		}
		wake(h, a.ID) // a dormant agent keeps its rank
		s.note(h, a.ID)
		h.agents.set(a.ID, &agentEntry{agent: a, lastEventMs: agentMs(h, a.LastEventAt)})
	}
	for _, c := range e.OpenCalls {
		if c.AgentID == "" || c.CallID == "" {
			continue
		}
		addCall(h, e.Host, c, s.ms(h, c.StartedAt))
	}
	for _, f := range files {
		// A confirmed file is a touch seed, not a write, so it suppresses no grey flicker.
		if f.Path == "" || f.Attribution == "confirmed" {
			continue
		}
		h.held = append(h.held, heldTouch{path: f.Path, atMs: s.ms(h, f.LastTouchAt)})
	}
}

// toolStart opens a call. Its files are not writes, so they record nothing.
func (s *Store) toolStart(h *hostState, host string, c *schema.Call, at int64) {
	if c == nil || c.AgentID == "" || c.CallID == "" {
		return
	}
	if prev, ok := h.calls.get(callKey{c.AgentID, c.CallID}); ok {
		closeOpen(h, prev)
	}
	addCall(h, host, *c, s.ms(h, c.StartedAt))
	if a, ok := h.agents.get(c.AgentID); ok {
		a.agent.Status = "working"
	}
	bump(h, c.AgentID, at)
	trimCalls(h)
}

func addCall(h *hostState, host string, c schema.Call, startMs int64) {
	c.Files = slices.Clone(c.Files)
	call := &ViewCall{Call: c, Host: host, StartMs: startMs}
	k := callKey{c.AgentID, c.CallID}
	h.calls.delete(k) // a restarted call moves to the newest position
	h.calls.set(k, call)
	open := h.openByAgent[c.AgentID]
	if open == nil {
		open = map[string]*ViewCall{}
		h.openByAgent[c.AgentID] = open
	}
	open[c.CallID] = call
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

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// omap is a map that iterates in insertion order, like a JavaScript Map.
// Setting an existing key keeps its position.
type omap[K comparable, V any] struct {
	idx     map[K]*list.Element
	entries list.List
}

type omapEntry[K comparable, V any] struct {
	key K
	val V
}

func newOmap[K comparable, V any]() *omap[K, V] {
	return &omap[K, V]{idx: map[K]*list.Element{}}
}

func (m *omap[K, V]) len() int { return len(m.idx) }

func (m *omap[K, V]) has(k K) bool {
	_, ok := m.idx[k]
	return ok
}

func (m *omap[K, V]) get(k K) (V, bool) {
	if e, ok := m.idx[k]; ok {
		return e.Value.(*omapEntry[K, V]).val, true
	}
	var zero V
	return zero, false
}

func (m *omap[K, V]) set(k K, v V) {
	if e, ok := m.idx[k]; ok {
		e.Value.(*omapEntry[K, V]).val = v
		return
	}
	m.idx[k] = m.entries.PushBack(&omapEntry[K, V]{key: k, val: v})
}

func (m *omap[K, V]) delete(k K) {
	if e, ok := m.idx[k]; ok {
		m.entries.Remove(e)
		delete(m.idx, k)
	}
}

func (m *omap[K, V]) first() (K, V, bool) {
	if e := m.entries.Front(); e != nil {
		en := e.Value.(*omapEntry[K, V])
		return en.key, en.val, true
	}
	var k K
	var v V
	return k, v, false
}

// all yields the entries in insertion order. The loop body may delete the current key only.
func (m *omap[K, V]) all() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for e := m.entries.Front(); e != nil; {
			next := e.Next()
			en := e.Value.(*omapEntry[K, V])
			if !yield(en.key, en.val) {
				return
			}
			e = next
		}
	}
}
