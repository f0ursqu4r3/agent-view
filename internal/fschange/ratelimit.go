package fschange

import "agent-view/internal/wire"

// windowMs is the length of one rate-limit window.
const windowMs = 1000

// RateLimiter allows at most perSecond events per key in a fixed one-second
// window that starts at the key's first event after the last window ended.
// It is not safe for concurrent use; Source guards it with its lock.
type RateLimiter struct {
	perSecond int
	now       func() int64
	windows   map[string]*rateWindow
}

type rateWindow struct {
	start int64
	count int
}

// NewRateLimiter returns a limiter that reads the time (Unix milliseconds)
// from now; nil means wire.NowMs.
func NewRateLimiter(perSecond int, now func() int64) *RateLimiter {
	if now == nil {
		now = wire.NowMs
	}
	return &RateLimiter{perSecond: perSecond, now: now, windows: map[string]*rateWindow{}}
}

// Allow reports whether one more event for key fits in its window, and
// counts it when it does.
func (r *RateLimiter) Allow(key string) bool {
	t := r.now()
	w := r.windows[key]
	if w == nil || t-w.start >= windowMs {
		w = &rateWindow{start: t}
		r.windows[key] = w
	}
	if w.count >= r.perSecond {
		return false
	}
	w.count++
	return true
}
