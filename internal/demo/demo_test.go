package demo

import (
	"strings"
	"testing"
	"time"

	"agent-view/internal/wire"
)

const startMs = 1_800_000_000_000.0

var validKinds = map[string]bool{
	"agent.upsert": true, "agent.status": true, "tool.start": true, "tool.update": true,
	"tool.output": true, "tool.end": true, "fs.change": true,
}

func validTs(ts string) bool {
	_, err := time.Parse(time.RFC3339, ts)
	return err == nil
}

// runDemo runs a demo on a FakeScheduler for ms virtual milliseconds.
func runDemo(scale string, ms float64) []wire.SourceEvent {
	var events []wire.SourceEvent
	sched := NewFakeScheduler(startMs)
	src := New(Options{Host: "h", Scale: scale, Sched: sched, Emit: func(b []wire.SourceEvent) { events = append(events, b...) }})
	src.Start()
	sched.Advance(ms)
	src.Stop()
	return events
}

func TestMulberry32(t *testing.T) {
	r := Mulberry32(20261117)
	for i, want := range []float64{0.876015507383272, 0.0866415249183774, 0.9978529647924006} {
		if got := r(); got != want {
			t.Errorf("draw %d = %v, want %v", i, got, want)
		}
	}
}

// FakeScheduler fires due callbacks by fire time, then creation order, and runs callbacks created during Advance
func TestFakeSchedulerOrder(t *testing.T) {
	f := NewFakeScheduler(1000)
	var got []string
	f.After(10, func() {
		got = append(got, "a")
		if f.NowMs() != 1010 {
			t.Errorf("now in a = %v", f.NowMs())
		}
		f.After(5, func() { got = append(got, "nested") })
	})
	f.After(5.5, func() { got = append(got, "b") })
	f.After(10, func() { got = append(got, "c") })
	cancel := f.After(1, func() { got = append(got, "cancelled") })
	cancel()
	f.After(100, func() { got = append(got, "late") })
	f.Advance(20)
	if want := "b,a,c,nested"; strings.Join(got, ",") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
	if f.NowMs() != 1020 {
		t.Errorf("now = %v, want 1020", f.NowMs())
	}
}

// DemoSource normal: valid events, paths under /demo, tools Read/Edit/Bash/Grep
func TestDemoSourceNormal(t *testing.T) {
	events := runDemo("normal", 4500)
	ids := map[string]bool{}
	upserts := 0
	for _, e := range events {
		if u, ok := e.Ev.(wire.AgentUpsert); ok {
			upserts++
			ids[u.Agent.ID] = true
		}
	}
	if upserts != 6 || len(ids) != 6 {
		t.Fatalf("upserts = %d, distinct ids = %d, want 6 and 6", upserts, len(ids))
	}
	for _, e := range events {
		if !validKinds[e.Ev.Kind()] {
			t.Errorf("invalid kind %q", e.Ev.Kind())
		}
		if !validTs(e.Ts) {
			t.Errorf("bad ts %q", e.Ts)
		}
	}
	var starts []wire.Call
	ended := map[string]bool{}
	categories := map[string]bool{}
	for _, e := range events {
		switch ev := e.Ev.(type) {
		case wire.ToolStart:
			starts = append(starts, ev.Call)
			categories[ev.Call.Category] = true
		case wire.ToolEnd:
			ended[ev.AgentID+"|"+ev.CallID] = true
			if ev.Writes != nil && (ev.Ok == nil || !*ev.Ok) {
				t.Errorf("writes on a call that is not ok: %+v", ev)
			}
		}
	}
	if len(starts) <= 5 {
		t.Errorf("starts = %d, want more than 5", len(starts))
	}
	if len(categories) < 3 {
		t.Errorf("categories = %v, want at least 3", categories)
	}
	open := 0
	for _, c := range starts {
		for _, f := range c.Files {
			if !strings.HasPrefix(f, "/demo/agent-view/") && !strings.HasPrefix(f, "/demo/search-service/") {
				t.Errorf("file outside /demo: %q", f)
			}
		}
		if !ended[c.AgentID+"|"+c.CallID] {
			open++
		}
	}
	if open > 6 {
		t.Errorf("%d calls still open, want at most 6", open)
	}
}

// DemoSource stress: 50 agents, 3,000 distinct files, about 200 events per second
func TestDemoSourceStress(t *testing.T) {
	distinct := map[string]bool{}
	for _, p := range FilePaths("stress") {
		distinct[p] = true
	}
	if len(distinct) != 3000 {
		t.Fatalf("distinct stress paths = %d, want 3000", len(distinct))
	}
	events := runDemo("stress", 2000)
	upserts, live := 0, 0
	roots := map[string]bool{}
	for _, e := range events {
		if u, ok := e.Ev.(wire.AgentUpsert); ok {
			upserts++
			roots[*u.Agent.ProjectRoot] = true
		} else {
			live++
		}
		if !validTs(e.Ts) {
			t.Errorf("bad ts %q", e.Ts)
		}
	}
	if upserts != 50 {
		t.Errorf("upserts = %d, want 50", upserts)
	}
	if len(roots) < 4 {
		t.Errorf("project roots = %d, want at least 4", len(roots))
	}
	if rate := float64(live) / 2; rate <= 120 || rate >= 320 {
		t.Errorf("live rate = %v per second, want between 120 and 320", rate)
	}
}

// DemoSource stop halts emission
func TestDemoSourceStop(t *testing.T) {
	var events []wire.SourceEvent
	sched := NewFakeScheduler(startMs)
	src := New(Options{Host: "h", Sched: sched, Emit: func(b []wire.SourceEvent) { events = append(events, b...) }})
	src.Start()
	sched.Advance(600)
	src.Stop()
	src.Stop()
	n := len(events)
	if n == 0 {
		t.Fatal("no events before stop")
	}
	sched.Advance(1500)
	if len(events) != n {
		t.Errorf("events after stop = %d, want %d", len(events), n)
	}
}

// a failing consumer must not stop the demo
func TestDemoSourceEmitPanic(t *testing.T) {
	calls := 0
	sched := NewFakeScheduler(startMs)
	src := New(Options{Host: "h", Sched: sched, Emit: func([]wire.SourceEvent) { calls++; panic("consumer failed") }})
	src.Start()
	sched.Advance(3000)
	src.Stop()
	if calls < 3 {
		t.Errorf("emit calls = %d, want at least 3", calls)
	}
}

// the real scheduler runs callbacks and honors cancel
func TestRealScheduler(t *testing.T) {
	s := NewRealScheduler()
	// NowMs first: the clock only moves on, so d is never negative.
	now := s.NowMs()
	if d := time.Now().UnixMilli() - int64(now); d < 0 || d > 1000 {
		t.Errorf("NowMs off by %d ms", d)
	}
	done := make(chan struct{})
	s.After(5, func() { close(done) })
	cancelled := make(chan struct{})
	cancel := s.After(5, func() { close(cancelled) })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not run")
	}
	select {
	case <-cancelled:
		t.Error("cancelled callback ran")
	case <-time.After(50 * time.Millisecond):
	}
}
