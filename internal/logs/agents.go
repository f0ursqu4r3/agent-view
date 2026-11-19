package logs

import "agent-view/internal/wire"

// identity is what an agent.upsert carries besides the fixed fields.
type identity struct {
	name     string
	role     string
	parentID *string
	link     string
	cwd      *string
	model    string // empty when absent
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (i identity) equal(o identity) bool {
	return i.name == o.name && i.role == o.role && samePtr(i.parentID, o.parentID) &&
		i.link == o.link && samePtr(i.cwd, o.cwd) && i.model == o.model
}

type agentState struct {
	identity
	status      string
	lastEventAt string
}

// emittedState is what the downstream consumer last saw for an agent.
type emittedState struct {
	identity identity
	status   string
}

// flushAgent emits agent.upsert when an identity field differs from what was
// last emitted for the agent.
func flushAgent(emitted map[string]*emittedState, provider, id string, s *agentState, ts string, out *[]wire.SourceEvent) {
	prev := emitted[id]
	ident := s.identity
	if prev != nil && prev.identity.equal(ident) {
		return
	}
	// The status in an upsert is the one the consumer will hold afterwards.
	status := "idle"
	if prev != nil {
		status = prev.status
	}
	emitted[id] = &emittedState{identity: ident, status: status}
	*out = append(*out, wire.SourceEvent{Ts: ts, Ev: wire.AgentUpsert{Agent: wire.Agent{
		ID: id, Provider: provider, Name: ident.name, Role: ident.role, ParentID: ident.parentID,
		Link: ident.link, Cwd: ident.cwd, Model: ident.model, Status: status, LastEventAt: s.lastEventAt,
	}}})
}

// flushStatus emits agent.status when the agent's status differs from what
// was last emitted.
func flushStatus(emitted map[string]*emittedState, id string, s *agentState, ts string, out *[]wire.SourceEvent) {
	prev := emitted[id]
	if prev == nil || prev.status == s.status {
		return
	}
	prev.status = s.status
	*out = append(*out, wire.SourceEvent{Ts: ts, Ev: wire.AgentStatusChange{AgentID: id, Status: s.status}})
}
