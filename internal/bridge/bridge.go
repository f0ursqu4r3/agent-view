// Package bridge feeds the hub's envelopes and host states to the TUI as
// Bubble Tea messages. It replaces the P1 collector child (internal/feed) for
// a collector and hub that run inside the TUI process.
package bridge

import (
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/feed"
	"agent-view/internal/hub"
	"agent-view/internal/safe"
	"agent-view/internal/schema"
	"agent-view/internal/wire"
)

const (
	defaultBatch    = 50 * time.Millisecond
	defaultMaxQueue = 50_000

	diagResync    = "bridge: the display fell behind; resynced from the hub"
	diagUndecoded = "bridge: skipped an envelope the TUI cannot decode"
)

// Config configures Start. Zero values select the defaults.
type Config struct {
	// Hub is the source of envelopes and host states.
	Hub *hub.Hub
	// Send delivers messages to the TUI; in main it is (*tea.Program).Send.
	// It is called only from the bridge's goroutine.
	Send func(tea.Msg)
	// Batch is the shortest time between two EnvelopesMsg (default 50 ms).
	Batch time.Duration
	// MaxQueue is the most entries the queue holds before the bridge drops
	// them and resyncs from the hub (default 50,000).
	MaxQueue int
	// Diag receives diagnostics lines. Nil drops them.
	Diag func(string)
}

// entry is one queued item: an encoded envelope line, or a host list.
type entry struct {
	line    []byte
	hosts   []wire.HostStatus
	isHosts bool
}

// Bridge subscribes to a hub and sends feed.EnvelopesMsg and feed.StateMsg.
//
// The hub subscriber runs under the hub's lock: it only encodes the envelope,
// appends the line to the queue and wakes the run goroutine. All decoding and
// every Send happen on the run goroutine.
type Bridge struct {
	c Config

	mu     sync.Mutex
	queue  []entry
	resync bool   // the queue overflowed; lines are ignored until the resync
	gen    uint64 // the generation of the current hub subscription
	closed bool   // the run goroutine is stopping; nothing more is queued

	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	// The fields below belong to the run goroutine (and to Start, before it
	// starts).
	unsub     func()
	offHosts  func()
	work      []entry
	pending   []schema.Envelope
	timer     *time.Timer
	sent      map[string]wire.HostStatus // the last state sent, per label
	undecoded int
}

// Start subscribes to c.Hub and starts the run goroutine. The first messages
// are one StateMsg per host in c.Hub.Hosts(); then come the envelopes and
// every later host state change, in order.
func Start(c Config) *Bridge {
	if c.Send == nil {
		c.Send = func(tea.Msg) {}
	}
	if c.Diag == nil {
		c.Diag = func(string) {}
	}
	if c.Batch <= 0 {
		c.Batch = defaultBatch
	}
	if c.MaxQueue <= 0 {
		c.MaxQueue = defaultMaxQueue
	}
	b := &Bridge{
		c:    c,
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
		sent: map[string]wire.HostStatus{},
	}
	// The listener comes first, so no change after the initial list is lost.
	b.offHosts = c.Hub.OnHostsChange(b.onHosts)
	initial := c.Hub.Hosts()
	b.mu.Lock()
	b.queue = append([]entry{{hosts: initial, isHosts: true}}, b.queue...)
	b.mu.Unlock()
	b.subscribe()
	go b.run()
	return b
}

// Stop sends the pending entries and batch, unsubscribes from the hub and
// waits for the run goroutine. After Stop returns, nothing more is sent. It
// waits for a Send in progress. A second call waits for the first.
func (b *Bridge) Stop() {
	b.stopOnce.Do(func() { close(b.stop) })
	<-b.done
}

// subscribe subscribes to the hub for the current generation. The hub replays
// hello, snapshot and coverage per host into the queue before it returns.
func (b *Bridge) subscribe() {
	b.mu.Lock()
	gen := b.gen
	b.mu.Unlock()
	b.unsub = b.c.Hub.Subscribe(func(e wire.Envelope) { b.onEnvelope(gen, e) })
}

// onEnvelope is the hub subscriber. It runs under the hub's lock, so it must
// not block, call Send, or call a hub method.
func (b *Bridge) onEnvelope(gen uint64, e wire.Envelope) {
	line := wire.EncodeLine(e)
	b.mu.Lock()
	if gen != b.gen || b.resync || b.closed {
		b.mu.Unlock()
		return
	}
	if len(b.queue) >= b.c.MaxQueue {
		b.queue = nil
		b.resync = true
	} else {
		b.queue = append(b.queue, entry{line: line})
	}
	b.mu.Unlock()
	b.poke()
}

// onHosts is the hub's host listener. It runs without the hub's lock.
func (b *Bridge) onHosts(hosts []wire.HostStatus) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.queue = append(b.queue, entry{hosts: hosts, isHosts: true})
	b.mu.Unlock()
	b.poke()
}

func (b *Bridge) poke() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// run is the bridge's goroutine. A panic is reported and the loop goes on.
func (b *Bridge) run() {
	defer close(b.done)
	for !b.loop() {
	}
}

// loop handles wake-ups, batch timers and the stop. It returns true after a
// stop and false after a recovered panic.
func (b *Bridge) loop() (stopped bool) {
	defer func() {
		if r := recover(); r != nil {
			b.diag(fmt.Sprintf("bridge: recovered a panic: %v", r))
			stopped = false
		}
	}()
	for {
		var tick <-chan time.Time
		if b.timer != nil {
			tick = b.timer.C
		}
		select {
		case <-b.stop:
			b.drain(false)
			b.flush()
			b.close()
			return true
		case <-b.wake:
			b.drain(true)
		case <-tick:
			b.flush()
		}
	}
}

// drain takes the queue and handles every entry. After an overflow it
// resyncs instead, when resync allows it.
func (b *Bridge) drain(resync bool) {
	b.mu.Lock()
	q, overflow := b.queue, b.resync
	b.queue = nil
	b.mu.Unlock()
	if overflow {
		if resync {
			b.resyncFromHub()
		}
		return
	}
	b.work = append(b.work, q...)
	for len(b.work) > 0 {
		e := b.work[0]
		b.work = b.work[1:]
		if e.isHosts {
			b.flush()
			b.sendHosts(e.hosts)
			continue
		}
		env, ok := schema.DecodeLine(e.line)
		if !ok {
			b.undecoded++
			if b.undecoded == 1 {
				b.diag(diagUndecoded)
			}
			continue
		}
		b.pending = append(b.pending, env)
		if b.timer == nil {
			b.timer = time.NewTimer(b.c.Batch)
		}
	}
}

// resyncFromHub replaces the subscription after an overflow. The envelopes
// not yet sent are dropped: the hub's replay (hello, snapshot and coverage
// per host) brings the TUI's store to the hub's state.
func (b *Bridge) resyncFromHub() {
	safe.Call(b.unsub)
	b.mu.Lock()
	b.gen++
	b.queue = nil
	b.resync = false
	b.mu.Unlock()
	b.work, b.pending = nil, nil
	b.stopTimer()
	b.subscribe()
	b.sent = map[string]wire.HostStatus{}
	b.sendHosts(b.c.Hub.Hosts())
	b.diag(diagResync)
}

// sendHosts sends a StateMsg for each host whose state or detail differs from
// the last one sent.
func (b *Bridge) sendHosts(hosts []wire.HostStatus) {
	for _, h := range hosts {
		if prev, ok := b.sent[h.Label]; ok && prev.State == h.State && prev.Detail == h.Detail {
			continue
		}
		b.sent[h.Label] = h
		b.c.Send(feed.StateMsg{Host: h.Label, State: h.State, Detail: h.Detail})
	}
}

// flush sends the pending batch, if any.
func (b *Bridge) flush() {
	b.stopTimer()
	if len(b.pending) == 0 {
		return
	}
	batch := b.pending
	b.pending = nil
	b.c.Send(feed.EnvelopesMsg(batch))
}

func (b *Bridge) stopTimer() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

// close ends the subscriptions. No lock is held while the hub is called.
func (b *Bridge) close() {
	b.mu.Lock()
	b.closed = true
	b.queue = nil
	b.mu.Unlock()
	unsub, offHosts := b.unsub, b.offHosts
	b.unsub, b.offHosts = nil, nil
	safe.Call(unsub)
	safe.Call(offHosts)
}

func (b *Bridge) diag(s string) { safe.Call(func() { b.c.Diag(s) }) }
