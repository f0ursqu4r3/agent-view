// Package fanout holds the subscriber core that collect, hub and remote share:
// a callback that a panic in it cannot break, and a flag that marks it
// removed. Each caller keeps its own list, its own lock, its own order and
// its own Removed check.
package fanout

import (
	"sync/atomic"

	"agent-view/internal/safe"
	"agent-view/internal/wire"
)

// Sub is one subscriber: a function that receives envelopes. A Sub is safe
// for concurrent use as far as Remove and Removed go. Send does not lock, so
// the caller orders its own calls to Send.
type Sub struct {
	fn      func(wire.Envelope)
	removed atomic.Bool
}

// NewSub returns a Sub that calls fn.
func NewSub(fn func(wire.Envelope)) *Sub {
	return &Sub{fn: fn}
}

// Send calls the function with env. A panic in the function is recovered, so
// that one failing subscriber does not break the others. Send does not check
// Removed: the caller decides, as it did before.
func (s *Sub) Send(env wire.Envelope) {
	safe.Call(func() { s.fn(env) })
}

// Remove marks the Sub removed. A second call changes nothing. The caller
// takes the Sub out of its own list.
func (s *Sub) Remove() { s.removed.Store(true) }

// Removed reports whether Remove was called.
func (s *Sub) Removed() bool { return s.removed.Load() }
