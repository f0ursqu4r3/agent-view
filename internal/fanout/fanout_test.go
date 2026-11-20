package fanout

import (
	"slices"
	"sync"
	"testing"

	"agent-view/internal/wire"
)

// A subscriber that panics must not break the caller: Send returns.
func TestSendSurvivesAPanic(t *testing.T) {
	calls := 0
	s := NewSub(func(wire.Envelope) {
		calls++
		panic("boom")
	})
	s.Send(wire.Envelope{Seq: 1})
	s.Send(wire.Envelope{Seq: 2})
	if calls != 2 {
		t.Errorf("fn ran %d times, want 2: a panic must not stop a later Send", calls)
	}
}

// Send calls fn once per call, in call order.
func TestSendCallsFnInOrder(t *testing.T) {
	var got []int64
	s := NewSub(func(env wire.Envelope) { got = append(got, env.Seq) })
	for _, seq := range []int64{1, 2, 3} {
		s.Send(wire.Envelope{Seq: seq})
	}
	if want := []int64{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("fn saw seq %v, want %v", got, want)
	}
}

// Removed is false until Remove, and a second Remove is safe. Send does not
// check Removed: each caller keeps its own check.
func TestRemoveSetsRemoved(t *testing.T) {
	calls := 0
	s := NewSub(func(wire.Envelope) { calls++ })
	if s.Removed() {
		t.Fatal("a new Sub reports Removed")
	}
	s.Remove()
	if !s.Removed() {
		t.Fatal("Removed is false after Remove")
	}
	s.Remove()
	if !s.Removed() {
		t.Fatal("Removed is false after a second Remove")
	}
	s.Send(wire.Envelope{})
	if calls != 1 {
		t.Errorf("fn ran %d times after Remove, want 1: Send must not check Removed", calls)
	}
}

// Remove and Removed may run on different goroutines (run with -race).
func TestRemovedIsSafeUnderRace(t *testing.T) {
	s := NewSub(func(wire.Envelope) {})
	var wg sync.WaitGroup
	wg.Go(s.Remove)
	wg.Go(func() {
		for !s.Removed() {
		}
	})
	wg.Wait()
	if !s.Removed() {
		t.Error("Removed is false after Remove returned")
	}
}
