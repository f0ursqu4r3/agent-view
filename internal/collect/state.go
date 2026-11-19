// Package collect holds the collector: the live-activity model and, in later
// files, the sources that feed it. It ports src/collector.
package collect

import (
	"cmp"
	"os"
	"slices"
	"strings"

	"agent-view/internal/lifecycle"
	"agent-view/internal/wire"
)

// endedLimit is the number of ended-call keys the state remembers.
const endedLimit = 20_000

type openCall struct {
	call        wire.Call
	lastEventMs int64
}

// State is the collector's model of live agent activity. Every source event
// goes through Apply. State is not safe for concurrent use.
type State struct {
	// Home is the home directory. WatchRoots never returns it.
	Home string

	opts   lifecycle.Options
	agents *omap[*wire.Agent]
	open   *omap[*openCall]
	files  map[string]wire.FileTouch
	// ended holds the keys of ended calls, oldest first. It only drops
	// duplicate events.
	ended *lruSet
}

// NewState returns an empty state. A zero field of o takes its default.
func NewState(o lifecycle.Options) *State {
	home, _ := os.UserHomeDir()
	return &State{
		Home:   home,
		opts:   o.WithDefaults(),
		agents: newOmap[*wire.Agent](),
		open:   newOmap[*openCall](),
		files:  map[string]wire.FileTouch{},
		ended:  newLRUSet(endedLimit),
	}
}

func callKey(agentID, callID string) string { return agentID + "|" + callID }

func cloneAgent(a wire.Agent) wire.Agent {
	if a.ParentID != nil {
		v := *a.ParentID
		a.ParentID = &v
	}
	if a.Cwd != nil {
		v := *a.Cwd
		a.Cwd = &v
	}
	if a.ProjectRoot != nil {
		v := *a.ProjectRoot
		a.ProjectRoot = &v
	}
	return a
}

func cloneCall(c wire.Call) wire.Call {
	c.Files = slices.Clone(c.Files)
	return c
}

// Apply updates the state with e. It returns the event to forward, or false
// when e is a duplicate.
func (s *State) Apply(e wire.SourceEvent) (wire.SourceEvent, bool) {
	switch ev := e.Ev.(type) {
	case wire.AgentUpsert:
		s.applyUpsert(ev, e.Ts)
	case wire.AgentStatusChange:
		if a, ok := s.agents.get(ev.AgentID); ok {
			a.Status = ev.Status
		}
		s.bump(ev.AgentID, e.Ts)
	case wire.ToolStart:
		return e, s.applyStart(ev, e.Ts)
	case wire.ToolUpdate:
		s.applyUpdate(ev, e.Ts)
	case wire.ToolOutput:
		if o, ok := s.open.get(callKey(ev.AgentID, ev.CallID)); ok {
			o.lastEventMs = max(o.lastEventMs, lifecycle.MsOf(e.Ts))
		}
		s.bump(ev.AgentID, e.Ts)
	case wire.ToolEnd:
		return e, s.applyEnd(ev, e.Ts)
	case wire.FsChange:
		s.touch(ev.Path, e.Ts, "unattributed", "")
	}
	return e, true
}

// Tick ends calls that have had no event for the call timeout, in the order
// they opened, and drops old file touches.
func (s *State) Tick(now int64) []wire.SourceEvent {
	var out []wire.SourceEvent
	s.open.each(func(key string, o *openCall) {
		if now-o.lastEventMs < s.opts.CallTimeoutMs {
			return
		}
		s.open.delete(key)
		s.ended.add(key)
		out = append(out, wire.SourceEvent{
			Ts: wire.FormatISO(now),
			Ev: wire.ToolEnd{AgentID: o.call.AgentID, CallID: o.call.CallID},
		})
	})
	for path, f := range s.files {
		if now-lifecycle.MsOf(f.LastTouchAt) >= s.opts.FileTTLMs {
			delete(s.files, path)
		}
	}
	return out
}

// Snapshot returns the present agents sorted by ID, their open calls sorted by
// start time and key, and the files touched within the file TTL sorted by
// path. The result shares nothing with the state.
func (s *State) Snapshot(now int64) wire.Snapshot {
	present := s.presentIDs(now)
	snap := wire.Snapshot{
		Agents:    []wire.Agent{},
		OpenCalls: []wire.Call{},
		Files:     []wire.FileTouch{},
	}
	s.agents.each(func(_ string, a *wire.Agent) {
		if present[a.ID] {
			snap.Agents = append(snap.Agents, cloneAgent(*a))
		}
	})
	slices.SortFunc(snap.Agents, func(a, b wire.Agent) int { return strings.Compare(a.ID, b.ID) })

	type keyed struct {
		key string
		o   *openCall
	}
	var calls []keyed
	s.open.each(func(k string, o *openCall) {
		if present[o.call.AgentID] {
			calls = append(calls, keyed{k, o})
		}
	})
	slices.SortFunc(calls, func(a, b keyed) int {
		return cmp.Or(
			cmp.Compare(lifecycle.MsOf(a.o.call.StartedAt), lifecycle.MsOf(b.o.call.StartedAt)),
			strings.Compare(a.key, b.key),
		)
	})
	for _, c := range calls {
		snap.OpenCalls = append(snap.OpenCalls, cloneCall(c.o.call))
	}

	for _, f := range s.files {
		if now-lifecycle.MsOf(f.LastTouchAt) < s.opts.FileTTLMs {
			snap.Files = append(snap.Files, f)
		}
	}
	slices.SortFunc(snap.Files, func(a, b wire.FileTouch) int { return strings.Compare(a.Path, b.Path) })
	return snap
}

// Load replaces all state with snap and clears the ended-call memory. A
// snapshot has no per-call event time, so an open call takes its agent's last
// event time when that is later than its start: a later Tick never ends a call
// before the collector itself would.
func (s *State) Load(snap wire.Snapshot) {
	s.agents = newOmap[*wire.Agent]()
	for _, a := range snap.Agents {
		c := cloneAgent(a)
		s.agents.set(c.ID, &c)
	}
	s.open = newOmap[*openCall]()
	for _, c := range snap.OpenCalls {
		agentMs := int64(0)
		if a, ok := s.agents.get(c.AgentID); ok {
			agentMs = lifecycle.MsOf(a.LastEventAt)
		}
		s.open.set(callKey(c.AgentID, c.CallID), &openCall{
			call:        cloneCall(c),
			lastEventMs: max(lifecycle.MsOf(c.StartedAt), agentMs),
		})
	}
	s.files = make(map[string]wire.FileTouch, len(snap.Files))
	for _, f := range snap.Files {
		s.files[f.Path] = f
	}
	s.ended = newLRUSet(endedLimit)
}

// WatchRoots returns the project roots of present agents, newest event first,
// at most limit. It never returns the home directory or "/".
func (s *State) WatchRoots(now int64, limit int) []string {
	present := s.presentIDs(now)
	var agents []*wire.Agent
	s.agents.each(func(_ string, a *wire.Agent) {
		if present[a.ID] {
			agents = append(agents, a)
		}
	})
	slices.SortStableFunc(agents, func(a, b *wire.Agent) int {
		return cmp.Compare(lifecycle.MsOf(b.LastEventAt), lifecycle.MsOf(a.LastEventAt))
	})
	var roots []string
	for _, a := range agents {
		if a.ProjectRoot == nil {
			continue
		}
		r := *a.ProjectRoot
		if r == "/" || (s.Home != "" && r == s.Home) || slices.Contains(roots, r) {
			continue
		}
		roots = append(roots, r)
		if len(roots) >= limit {
			break
		}
	}
	return roots
}

// Agent returns a copy of the agent with the given ID.
func (s *State) Agent(id string) (wire.Agent, bool) {
	a, ok := s.agents.get(id)
	if !ok {
		return wire.Agent{}, false
	}
	return cloneAgent(*a), true
}

// FileCount returns the number of file touches held, including those Snapshot
// already leaves out. It is a diagnostic.
func (s *State) FileCount() int { return len(s.files) }

func (s *State) presentIDs(now int64) map[string]bool {
	agents := make([]wire.Agent, 0, len(s.agents.idx))
	s.agents.each(func(_ string, a *wire.Agent) { agents = append(agents, *a) })
	return lifecycle.PresentAgentIDs(agents, now, s.opts)
}

func (s *State) bump(agentID, ts string) {
	if a, ok := s.agents.get(agentID); ok {
		a.LastEventAt = lifecycle.LaterISO(a.LastEventAt, ts)
	}
}

// touch records a file touch. The newest touch wins. On a tie, a confirmed
// touch replaces an unattributed one.
func (s *State) touch(path, ts, attribution, agentID string) {
	if cur, ok := s.files[path]; ok {
		diff := lifecycle.MsOf(ts) - lifecycle.MsOf(cur.LastTouchAt)
		if diff < 0 || (diff == 0 && !(attribution == "confirmed" && cur.Attribution == "unattributed")) {
			return
		}
	}
	f := wire.FileTouch{Path: path, LastTouchAt: ts, Attribution: attribution}
	if attribution == "confirmed" {
		f.AgentID = agentID
	}
	s.files[path] = f
}

func (s *State) applyUpsert(e wire.AgentUpsert, ts string) {
	in := e.Agent
	seen := lifecycle.LaterISO(in.LastEventAt, ts)
	cur, ok := s.agents.get(in.ID)
	if !ok {
		c := cloneAgent(in)
		c.LastEventAt = seen
		s.agents.set(c.ID, &c)
		return
	}
	// Copy the descriptive fields. Status and lastEventAt stay. A missing
	// model does not erase the known one.
	d := cloneAgent(in)
	cur.Provider, cur.Name, cur.Role, cur.ParentID = d.Provider, d.Name, d.Role, d.ParentID
	cur.Link, cur.Cwd, cur.ProjectRoot = d.Link, d.Cwd, d.ProjectRoot
	if d.Model != "" {
		cur.Model = d.Model
	}
	cur.LastEventAt = lifecycle.LaterISO(cur.LastEventAt, seen)
}

func (s *State) applyStart(e wire.ToolStart, ts string) bool {
	c := e.Call
	key := callKey(c.AgentID, c.CallID)
	if s.open.has(key) || s.ended.has(key) {
		if s.ended.has(key) {
			s.ended.add(key)
		}
		return false
	}
	s.open.set(key, &openCall{call: cloneCall(c), lastEventMs: lifecycle.MsOf(ts)})
	if a, ok := s.agents.get(c.AgentID); ok {
		a.Status = "working"
	}
	s.bump(c.AgentID, ts)
	if c.Category != "search" {
		for _, p := range c.Files {
			s.touch(p, ts, "confirmed", c.AgentID)
		}
	}
	return true
}

func (s *State) applyUpdate(e wire.ToolUpdate, ts string) {
	key := callKey(e.AgentID, e.CallID)
	o, isOpen := s.open.get(key)
	s.bump(e.AgentID, ts)
	switch {
	case isOpen:
		o.lastEventMs = max(o.lastEventMs, lifecycle.MsOf(ts))
		merged := make([]string, 0, len(o.call.Files)+len(e.Files))
		for _, p := range slices.Concat(o.call.Files, e.Files) {
			if !slices.Contains(merged, p) {
				merged = append(merged, p)
			}
		}
		o.call.Files = merged
		if o.call.Category != "search" {
			for _, p := range e.Files {
				s.touch(p, ts, "confirmed", e.AgentID)
			}
		}
	case !s.ended.has(key):
		for _, p := range e.Files {
			s.touch(p, ts, "confirmed", e.AgentID)
		}
	}
}

func (s *State) applyEnd(e wire.ToolEnd, ts string) bool {
	key := callKey(e.AgentID, e.CallID)
	if s.ended.has(key) {
		s.ended.add(key)
		return false
	}
	s.open.delete(key)
	s.ended.add(key)
	s.bump(e.AgentID, ts)
	for _, w := range e.Writes {
		s.touch(w.Path, ts, "confirmed", e.AgentID)
	}
	return true
}
