package tui

import (
	"fmt"
	"testing"
	"time"

	"agent-view/internal/anim"
	"agent-view/internal/board"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

// stress feeds its hosts with 50 agents (10 main, 4 sub-agents each) and a
// generated mix of tool events. Main agent m and its sub-agents live on host
// m modulo the number of hosts.
type stress struct {
	s      *store.Store
	log    *board.Log
	clock  int64
	seq    int64
	hosts  []string
	agents []string
	hostOf map[string]string
	call   int
	open   []int // calls the last batch left open
}

var stressCats = []string{"read", "edit", "write", "command", "search", "agent", "other"}

func newStress(b *testing.B, hosts ...string) *stress {
	b.Helper()
	st := &stress{log: &board.Log{}, clock: nowMs, hosts: hosts, hostOf: map[string]string{}}
	st.s = store.New(store.Options{Now: func() int64 { return st.clock }})
	for m := 0; m < 10; m++ {
		host := hosts[m%len(hosts)]
		mid := fmt.Sprintf("0f3a%04x-5c1e-4b7d-9a2e-%012x", m, m*7919)
		st.add(host, mid, "")
		for k := 0; k < 4; k++ {
			st.add(host, fmt.Sprintf("agent-a%03x%02x%08x", m, k, (m*4+k)*104729), mid)
		}
	}
	return st
}

// apply sends e from the host of its agent.
func (st *stress) apply(host string, e schema.Envelope) {
	st.seq++
	e.V, e.Host, e.Seq, e.Ts = 1, host, st.seq, iso(st.clock)
	for _, c := range st.s.Apply(e) {
		st.log.FromChange(st.s, c)
	}
}

func (st *stress) add(host, id, parent string) {
	a := schema.Agent{
		ID: id, Provider: "claude", Name: fmt.Sprintf("session %02d", len(st.agents)), Role: "main", Link: "root", Model: "sonnet",
		Cwd: ptr("/work/repo/pkg"), ProjectRoot: ptr("/work/repo"), Status: "working", LastEventAt: iso(st.clock),
	}
	if parent != "" {
		a.ParentID, a.Role, a.Link = ptr(parent), "sub-agent", "confirmed"
	}
	st.hostOf[id] = host
	st.apply(host, schema.Envelope{Kind: "agent.upsert", Agent: &a})
	st.agents = append(st.agents, id)
}

// callOf returns the agent and call id of call number n.
func (st *stress) callOf(n int) (string, string) {
	return st.agents[n%len(st.agents)], fmt.Sprintf("toolu_%08d", n)
}

func (st *stress) start(n int) {
	agent, id := st.callOf(n)
	cat := stressCats[n%len(stressCats)]
	c := schema.Call{
		AgentID: agent, CallID: id, StartedAt: iso(st.clock), Category: cat, RawName: "Tool",
		Files: []string{fmt.Sprintf("/work/repo/pkg/file%d.go", n%97)},
	}
	if cat == "command" {
		c.Command = fmt.Sprintf("go test ./pkg%d/... -run Test%d -count=1", n%13, n)
	}
	st.apply(st.hostOf[agent], schema.Envelope{Kind: "tool.start", Call: &c})
}

func (st *stress) update(n int) {
	agent, id := st.callOf(n)
	st.apply(st.hostOf[agent], schema.Envelope{
		Kind: "tool.update", AgentID: agent, CallID: id,
		RawFiles: []byte(fmt.Sprintf(`["/work/repo/pkg/file%d.go","/work/repo/pkg/extra%d.go"]`, n%97, n%11)),
	})
}

func (st *stress) output(n int) {
	agent, id := st.callOf(n)
	st.apply(st.hostOf[agent], schema.Envelope{
		Kind: "tool.output", AgentID: agent, CallID: id,
		Chunk: fmt.Sprintf("ok  \tagent-view/pkg%d\t0.%03ds\n", n%13, n%1000),
	})
}

func (st *stress) end(n int) {
	agent, id := st.callOf(n)
	e := schema.Envelope{
		Kind: "tool.end", AgentID: agent, CallID: id, Ok: ptr(n%7 != 0),
		Writes: []schema.Write{{Path: fmt.Sprintf("/work/repo/pkg/file%d.go", n%97), Op: "edit"}},
	}
	if n%7 == 0 {
		e.ExitCode = ptr(1)
	}
	st.apply(st.hostOf[agent], e)
}

// batch applies exactly 200 envelopes: it ends the 10 calls the previous batch
// left open, then starts 50 calls, updates each, gives each output, and ends 40
// of them. Every fifth call stays open so rows show live cards. The store stays
// at about 10 open calls and a full host of ended calls, whatever b.N is.
func (st *stress) batch() {
	const total = 200
	from := st.seq
	for _, n := range st.open {
		st.end(n)
	}
	st.open = st.open[:0]
	base := st.call
	for i := 0; i < 50; i++ {
		st.start(base + i)
	}
	for i := 0; i < 50; i++ {
		st.update(base + i)
		st.output(base + i)
	}
	for i := 0; i < 50; i++ {
		if i%5 == 0 {
			st.open = append(st.open, base+i)
		} else {
			st.end(base + i)
		}
	}
	// Pad to 200 with output chunks on the open calls.
	for i := 0; st.seq-from < total; i++ {
		st.output(st.open[i%len(st.open)])
	}
	if st.seq-from != total {
		panic("batch is not 200 envelopes")
	}
	st.call += 50
	st.clock += 1000
}

// fill gives every host a full history of ended calls: at least its cap of 2,000.
func (st *stress) fill() {
	for !st.full() {
		st.start(st.call)
		st.end(st.call)
		st.call++
		st.clock += 20
	}
}

func (st *stress) full() bool {
	for _, h := range st.hosts {
		if _, calls := st.s.HostStats(h); calls < 2000 {
			return false
		}
	}
	return true
}

// frames applies one batch of 200 envelopes and composes one frame per
// iteration. ns/op covers both; compose-ns/op is the frame alone.
func frames(b *testing.B, st *stress) {
	b.Helper()
	st.fill()
	var hosts []HostStatus
	for _, h := range st.hosts {
		hosts = append(hosts, HostStatus{Label: h, State: "up"})
	}
	in := Input{
		Store: st.s, Anim: anim.New(false), Log: st.log, W: 200, H: 60,
		Status: Status{Hosts: hosts, Agents: 50, Working: 50, Clock: "01:00:00"},
	}
	var composeNs int64
	// Outside the timed loop: the frame must draw real rows.
	st.batch()
	in.Now = st.clock
	if g, hits := Compose(in); g.W != 200 || g.H != 60 || len(hits) == 0 {
		b.Fatalf("Compose drew no rows: %dx%d, %d hits", g.W, g.H, len(hits))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.batch()
		in.Now = st.clock
		in.Spin = i
		t0 := time.Now()
		Compose(in)
		composeNs += time.Since(t0).Nanoseconds()
	}
	b.StopTimer()
	b.ReportMetric(float64(composeNs)/float64(b.N), "compose-ns/op")
	if st.s.Panics != 0 {
		b.Fatalf("Apply recovered %d panic(s)", st.s.Panics)
	}
	// The store must not drift with b.N: about 10 open calls, a full history on every host.
	open := 0
	for _, id := range st.agents {
		open += len(st.s.OpenCalls(store.AgentKey{Host: st.hostOf[id], ID: id}))
	}
	if open != 10 || !st.full() {
		b.Fatalf("store drifted: %d open calls, hosts full = %v", open, st.full())
	}
}

func BenchmarkComposeStress(b *testing.B) {
	frames(b, newStress(b, "devbox"))
}

// BenchmarkFrame50Agents3Hosts200Envelopes is the stress frame with the 50
// agents spread over 3 hosts, each with a full history of calls.
func BenchmarkFrame50Agents3Hosts200Envelopes(b *testing.B) {
	frames(b, newStress(b, "devbox", "box", "box#2"))
}
