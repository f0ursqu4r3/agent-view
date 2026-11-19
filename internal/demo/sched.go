package demo

import (
	"sync"
	"sync/atomic"
	"time"
)

// Scheduler is the demo's clock and timer source. Tests and the parity trace
// drive a FakeScheduler in virtual time.
type Scheduler interface {
	// NowMs is the current time in Unix milliseconds.
	NowMs() float64
	// After runs fn once, ms milliseconds from now. The returned function
	// cancels the timer.
	After(ms float64, fn func()) (cancel func())
}

type realScheduler struct {
	queue chan *realTimer
}

type realTimer struct {
	fn        func()
	cancelled atomic.Bool
}

// NewRealScheduler returns a Scheduler on the wall clock. One goroutine runs
// every callback, so callbacks never run at the same time.
func NewRealScheduler() Scheduler {
	s := &realScheduler{queue: make(chan *realTimer, 64)}
	go func() {
		for t := range s.queue {
			if !t.cancelled.Load() {
				t.fn()
			}
		}
	}()
	return s
}

func (s *realScheduler) NowMs() float64 { return float64(time.Now().UnixMilli()) }

func (s *realScheduler) After(ms float64, fn func()) func() {
	t := &realTimer{fn: fn}
	timer := time.AfterFunc(time.Duration(ms*float64(time.Millisecond)), func() { s.queue <- t })
	return func() {
		t.cancelled.Store(true)
		timer.Stop()
	}
}

// FakeScheduler is a Scheduler in virtual time. Only Advance moves the clock.
type FakeScheduler struct {
	mu     sync.Mutex
	now    float64
	seq    int
	timers []*fakeTimer
}

type fakeTimer struct {
	at  float64
	seq int
	fn  func()
}

// NewFakeScheduler returns a FakeScheduler whose clock starts at startMs.
func NewFakeScheduler(startMs float64) *FakeScheduler {
	return &FakeScheduler{now: startMs}
}

// NowMs is the virtual time.
func (f *FakeScheduler) NowMs() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After schedules fn at now + ms, keeping the fraction.
func (f *FakeScheduler) After(ms float64, fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{at: f.now + ms, seq: f.seq, fn: fn}
	f.seq++
	f.timers = append(f.timers, t)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.remove(t)
	}
}

func (f *FakeScheduler) remove(t *fakeTimer) {
	for i, x := range f.timers {
		if x == t {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			return
		}
	}
}

// Advance moves the clock ms forward. It fires every callback that falls due,
// in order of fire time and then creation order. The clock reads the fire time
// while a callback runs. A callback created during Advance fires in the same
// call when it is due. At the end the clock is exactly start + ms.
func (f *FakeScheduler) Advance(ms float64) {
	f.mu.Lock()
	target := f.now + ms
	f.mu.Unlock()
	for {
		f.mu.Lock()
		var next *fakeTimer
		for _, t := range f.timers {
			if t.at > target {
				continue
			}
			if next == nil || t.at < next.at || (t.at == next.at && t.seq < next.seq) {
				next = t
			}
		}
		if next == nil {
			f.now = target
			f.mu.Unlock()
			return
		}
		f.remove(next)
		f.now = next.at
		f.mu.Unlock()
		next.fn()
	}
}
