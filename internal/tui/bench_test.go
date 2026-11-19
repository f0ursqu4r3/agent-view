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

// stress feeds one host with 50 agents (10 main, 4 sub-agents each) and a
// generated mix of tool events.
type stress struct {
	s      *store.Store
	log    *board.Log
	clock  int64
	seq    int64
	agents []string
	call   int
}

var stressCats = []string{"read", "edit", "write", "command", "search", "agent", "other"}

func newStress(b *testing.B) *stress {
	b.Helper()
	st := &stress{log: &board.Log{}, clock: nowMs}
	st.s = store.New(store.Options{Now: func() int64 { return st.clock }})
	for m := 0; m < 10; m++ {
		mid := fmt.Sprintf("0f3a%04x-5c1e-4b7d-9a2e-%012x", m, m*7919)
		st.add(mid, "")
		for k := 0; k < 4; k++ {
			st.add(fmt.Sprintf("agent-a%03x%02x%08x", m, k, (m*4+k)*104729), mid)
		}
	}
	return st
}

func (st *stress) apply(e schema.Envelope) {
	st.seq++
	e.V, e.Host, e.Seq, e.Ts = 1, "devbox", st.seq, iso(st.clock)
	for _, c := range st.s.Apply(e) {
		st.log.FromChange(st.s, c)
	}
}

func (st *stress) add(id, parent string) {
	a := schema.Agent{
		ID: id, Provider: "claude", Name: fmt.Sprintf("session %02d", len(st.agents)), Role: "main", Link: "root", Model: "sonnet",
		Cwd: ptr("/work/repo/pkg"), ProjectRoot: ptr("/work/repo"), Status: "working", LastEventAt: iso(st.clock),
	}
	if parent != "" {
		a.ParentID, a.Role, a.Link = ptr(parent), "sub-agent", "confirmed"
	}
	st.apply(schema.Envelope{Kind: "agent.upsert", Agent: &a})
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
	st.apply(schema.Envelope{Kind: "tool.start", Call: &c})
}

func (st *stress) update(n int) {
	agent, id := st.callOf(n)
	st.apply(schema.Envelope{
		Kind: "tool.update", AgentID: agent, CallID: id,
		RawFiles: []byte(fmt.Sprintf(`["/work/repo/pkg/file%d.go","/work/repo/pkg/extra%d.go"]`, n%97, n%11)),
	})
}

func (st *stress) output(n int) {
	agent, id := st.callOf(n)
	st.apply(schema.Envelope{
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
	st.apply(e)
}

// batch applies 200 envelopes: 50 calls, each started, updated, given output,
// and ended in the same batch; the calls overlap, so some are open at the end.
func (st *stress) batch() {
	base := st.call
	for i := 0; i < 50; i++ {
		st.start(base + i)
	}
	for i := 0; i < 50; i++ {
		st.update(base + i)
		st.output(base + i)
	}
	// Leave every fifth call open so rows show live cards.
	ended := 0
	for i := 0; i < 50; i++ {
		if i%5 != 0 {
			st.end(base + i)
			ended++
		}
	}
	// Reach 200 envelopes with output chunks on the open calls.
	for i := 0; 50+100+ended+i < 200; i++ {
		st.output(base + (i*5)%50)
	}
	st.call += 50
	st.clock += 1000
}

func BenchmarkComposeStress(b *testing.B) {
	st := newStress(b)
	// History: fill the host to its cap of 2,000 calls, all ended.
	for i := 0; i < 2000; i++ {
		st.start(st.call)
		st.end(st.call)
		st.call++
		st.clock += 20
	}
	in := Input{
		Store: st.s, Anim: anim.New(false), Log: st.log, W: 200, H: 60,
		Status: Status{Hosts: []HostStatus{{Label: "devbox", State: "up"}}, Agents: 50, Working: 50, Clock: "01:00:00"},
	}
	var composeNs int64
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
	// ns/op covers the 200 envelopes and the frame; compose-ns/op is the frame alone.
	b.ReportMetric(float64(composeNs)/float64(b.N), "compose-ns/op")
	if st.s.Panics != 0 {
		b.Fatalf("Apply recovered %d panic(s)", st.s.Panics)
	}
}
