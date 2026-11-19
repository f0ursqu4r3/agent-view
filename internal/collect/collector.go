package collect

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"agent-view/internal/demo"
	"agent-view/internal/fschange"
	"agent-view/internal/lifecycle"
	"agent-view/internal/logs"
	"agent-view/internal/wire"
)

const (
	tickInterval     = 1000 * time.Millisecond
	coverageInterval = 5000 * time.Millisecond
	maxWatchRoots    = 24
)

// Options configure a Collector.
type Options struct {
	Host string
	// Demo runs only the demo source, at DemoScale ("normal" or "stress").
	Demo      bool
	DemoScale string
	// Watch are directories to watch for file changes besides the project
	// roots of present agents.
	Watch []string
	// Lifecycle are the state's durations; a zero field takes its default.
	Lifecycle lifecycle.Options
	// Roots are the log roots; nil means logs.DiscoverRoots.
	Roots []logs.Root
	// Sched is the demo's clock; nil means the wall clock.
	Sched demo.Scheduler
	// Diag receives the collector's diagnostics, one message at a time and
	// without a prefix or a newline: a step that failed ("collector step
	// failed: <v>"), watch roots that cannot be updated ("cannot update watch
	// roots: <message>") and a snapshot that cannot be built ("snapshot
	// failed: <v>"). Nil writes each one to os.Stderr as "agent-view: <msg>"
	// and a newline.
	//
	// The collector calls Diag on its loop goroutine (a snapshot failure while
	// a stopped collector greets a subscriber: on the caller's goroutine), so
	// Diag must return quickly. It must not call back into the collector:
	// Start, Stop, Subscribe and the like wait for the loop. A panic in Diag
	// is recovered and dropped.
	Diag func(msg string)
}

// Collector is one collector. It ports src/collector/collector.ts. The
// sources feed one ordered queue; one loop goroutine takes each event from
// it, enriches it (not in demo mode), applies it to the state, wraps it in
// an envelope and delivers it to every subscriber in order. A demo
// collector runs only the demo source.
//
// The sources hand over their batches and statuses without ever blocking:
// a batch joins a mutex-guarded queue and wakes the loop. The loop
// goroutine owns the state, the sequence number, the path caches and the
// subscribers, and it is the only caller of the file-change source's
// SetRoots and of the subscriber functions.
type Collector struct {
	opts  Options
	state *State
	paths *paths

	// stepHook is a test hook. It is nil in production. When set, the loop
	// calls it at the start of every queued step, inside the step's
	// recover. Set it before Start or Subscribe.
	stepHook func()

	// Owned by the loop goroutine.
	seq       int64
	subs      []*subscriber
	ticker    *time.Ticker
	covTicker *time.Ticker

	loopOnce  sync.Once
	wake      chan struct{} // holds one token while the queue may have items
	quit      chan struct{} // closed by the first Stop
	loopDone  chan struct{} // closed when the loop has ended or will never run
	stopDone  chan struct{} // closed when the first Stop has finished
	startOnce sync.Once
	startErr  error

	// srcMu guards the sources. Start holds it while it creates them, so
	// Stop never misses one.
	srcMu sync.Mutex
	logs  *logs.Source
	fs    *fschange.Source
	demo  *demo.Source

	// mu guards the fields below. It is never held while other code runs.
	mu      sync.Mutex
	queue   []item
	stopped bool
	sources map[string]wire.SourceState
}

// item is one entry of the loop's queue: a source batch, a subscriber to
// greet, or work to run on the loop. done, when set, is closed once the
// item is handled.
type item struct {
	events []wire.SourceEvent
	sub    *subscriber
	run    func()
	done   chan struct{}
}

type subscriber struct {
	fn      func(wire.Envelope)
	removed atomic.Bool
}

func (s *subscriber) unsubscribe() { s.removed.Store(true) }

// send calls the subscriber. A subscriber that panics must not break the
// others.
func (s *subscriber) send(env wire.Envelope) {
	defer func() { _ = recover() }()
	s.fn(env)
}

// New returns a collector that does nothing until Start or Subscribe.
func New(o Options) *Collector {
	o.Watch = slices.Clone(o.Watch)
	o.Roots = slices.Clone(o.Roots)
	return &Collector{
		opts:     o,
		state:    NewState(o.Lifecycle),
		paths:    newPaths(),
		wake:     make(chan struct{}, 1),
		quit:     make(chan struct{}),
		loopDone: make(chan struct{}),
		stopDone: make(chan struct{}),
		sources:  map[string]wire.SourceState{},
	}
}

// Start starts the sources. It returns once the events of the first log
// scan (in demo mode, the demo's first events) are in the state. A second
// call waits for the first one; a call after Stop does nothing.
func (c *Collector) Start() error {
	c.startOnce.Do(func() { c.startErr = c.run() })
	return c.startErr
}

// Stop stops the sources, then the loop. It is idempotent, and it may run
// while Start is still in the first scan. What is still queued is dropped,
// and nothing is delivered after it returns: it waits for a subscriber
// function that is running.
func (c *Collector) Stop() {
	c.mu.Lock()
	first := !c.stopped
	c.stopped = true
	c.queue = nil
	c.mu.Unlock()
	if !first {
		<-c.stopDone
		return
	}
	c.srcMu.Lock()
	d, lg, fs := c.demo, c.logs, c.fs
	c.srcMu.Unlock()
	if d != nil {
		d.Stop()
	}
	if lg != nil {
		lg.Stop()
	}
	if fs != nil {
		fs.Stop()
	}
	close(c.quit)
	c.loopOnce.Do(func() { close(c.loopDone) }) // the loop never ran
	<-c.loopDone
	// The loop may have set the roots while the first fs.Stop ran.
	if fs != nil {
		fs.Stop()
	}
	close(c.stopDone)
}

// Subscribe sends fn hello, then snapshot, then every later envelope.
// hello and snapshot carry the current sequence number without taking a
// number, so other subscribers see no gap. It returns once both are
// delivered, and returns the unsubscribe function.
//
// fn runs on the loop goroutine (after Stop, on the caller's). The loop
// waits for it, so it should return quickly, and it must not call Start,
// Stop or Subscribe, which wait for the loop; it may call them on another
// goroutine (go c.Stop()).
func (c *Collector) Subscribe(fn func(wire.Envelope)) (unsubscribe func()) {
	s := &subscriber{fn: fn}
	c.ensureLoop()
	done := make(chan struct{})
	if c.enqueue(item{sub: s, done: done}) {
		select {
		case <-done:
			return s.unsubscribe
		case <-c.loopDone:
		}
		select {
		case <-done:
			return s.unsubscribe
		default:
		}
	} else {
		<-c.loopDone
	}
	// The collector stopped before the loop took the request. The state no
	// longer changes, so greet here, as TypeScript does after stop.
	hello, snap := c.greeting()
	s.send(hello)
	s.send(snap)
	return s.unsubscribe
}

// Coverage describes what the collector watches. It is safe from any
// goroutine.
func (c *Collector) Coverage() wire.Coverage {
	c.srcMu.Lock()
	lg, fs := c.logs, c.fs
	c.srcMu.Unlock()
	cov := wire.Coverage{LogRoots: []string{}, WatchRoots: []string{}}
	if lg != nil {
		lc := lg.Coverage()
		cov.LogRoots = lc.LogRoots
		cov.LogsOpen, cov.LogsDiscovered, cov.MalformedRecords = lc.LogsOpen, lc.LogsDiscovered, lc.MalformedRecords
	}
	if fs != nil {
		cov.WatchRoots = fs.Roots()
		cov.FSDropped = fs.Dropped()
	}
	c.mu.Lock()
	cov.Sources = maps.Clone(c.sources)
	c.mu.Unlock()
	return cov
}

func (c *Collector) run() error {
	if c.isStopped() {
		return nil
	}
	c.ensureLoop()
	c.enqueue(item{run: c.startTimers})
	if c.opts.Demo {
		d := demo.New(demo.Options{Host: c.opts.Host, Scale: c.opts.DemoScale, Emit: c.push, Sched: c.opts.Sched})
		c.srcMu.Lock()
		if !c.isStopped() {
			c.demo = d
			d.Start()
		}
		c.srcMu.Unlock()
		c.pushCoverage()
		c.await(nil)
		return nil
	}
	roots := c.opts.Roots
	if roots == nil {
		roots = logs.DiscoverRoots(os.Getenv, c.state.Home)
	}
	c.srcMu.Lock()
	if c.isStopped() {
		c.srcMu.Unlock()
		return nil
	}
	c.fs = fschange.New(fschange.Options{Emit: c.push, OnStatus: c.onStatus})
	c.logs = logs.NewSource(logs.SourceOptions{Host: c.opts.Host, Roots: roots, Emit: c.push, OnStatus: c.onStatus})
	lg := c.logs
	c.srcMu.Unlock()
	c.await(c.syncRoots)
	if err := lg.Start(); err != nil {
		return err
	}
	if c.isStopped() {
		return nil
	}
	c.pushCoverage()
	// The first scan's events are in the state when Start returns.
	c.await(nil)
	return nil
}

func (c *Collector) isStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// enqueue adds it to the queue and wakes the loop without blocking. It
// reports false once the collector is stopped.
func (c *Collector) enqueue(it item) bool {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return false
	}
	c.queue = append(c.queue, it)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

// push is the sources' Emit. It never blocks.
func (c *Collector) push(events []wire.SourceEvent) {
	if len(events) > 0 {
		c.enqueue(item{events: events})
	}
}

// onStatus is the sources' OnStatus. It never blocks.
func (c *Collector) onStatus(source string, s wire.SourceState) {
	c.mu.Lock()
	c.sources[source] = s
	c.mu.Unlock()
	c.push([]wire.SourceEvent{{Ts: wire.FormatISO(wire.NowMs()), Ev: wire.SourceStatus{Source: source, SourceState: s}}})
}

func (c *Collector) pushCoverage() {
	cov := c.Coverage()
	c.push([]wire.SourceEvent{{Ts: wire.FormatISO(wire.NowMs()), Ev: wire.CoverageReport{Coverage: cov}}})
}

// await runs fn (when not nil) on the loop after every item queued before
// it, and waits for it. It reports false when the loop ended first.
func (c *Collector) await(fn func()) bool {
	done := make(chan struct{})
	if !c.enqueue(item{run: fn, done: done}) {
		return false
	}
	select {
	case <-done:
		return true
	case <-c.loopDone:
		return false
	}
}

// Loop goroutine

func (c *Collector) ensureLoop() {
	c.loopOnce.Do(func() { go c.loop() })
}

func (c *Collector) loop() {
	defer close(c.loopDone)
	defer func() {
		if c.ticker != nil {
			c.ticker.Stop()
			c.covTicker.Stop()
		}
	}()
	for {
		var tickC, covC <-chan time.Time
		if c.ticker != nil {
			tickC, covC = c.ticker.C, c.covTicker.C
		}
		select {
		case <-c.quit:
			return
		case <-c.wake:
		case <-tickC:
			c.enqueue(item{run: c.tick})
		case <-covC:
			c.pushCoverage()
		}
		if !c.drain() {
			return
		}
	}
}

func (c *Collector) startTimers() {
	c.ticker = time.NewTicker(tickInterval)
	c.covTicker = time.NewTicker(coverageInterval)
}

// pollTimers queues a tick or a coverage report for a timer that has fired,
// without blocking. drain calls it between batches, so a queue that never
// empties still runs them at their interval, behind the items queued before
// them (as TypeScript timers join the queue tail).
func (c *Collector) pollTimers() {
	if c.ticker == nil || c.covTicker == nil {
		return
	}
	select {
	case <-c.ticker.C:
		c.enqueue(item{run: c.tick})
	default:
	}
	select {
	case <-c.covTicker.C:
		c.pushCoverage()
	default:
	}
}

// drain handles the queued items in order. It reports false once the
// collector is stopped; the items left are dropped.
func (c *Collector) drain() bool {
	for {
		c.pollTimers()
		c.mu.Lock()
		items := c.queue
		c.queue = nil
		stopped := c.stopped
		c.mu.Unlock()
		if stopped {
			return false
		}
		if len(items) == 0 {
			return true
		}
		for _, it := range items {
			if c.isStopped() {
				return false
			}
			c.handle(it)
		}
	}
}

// handle runs one queued step. A panic in the step is recovered and reported
// (as TypeScript's .catch swallows it), and the item's done channel still
// closes.
func (c *Collector) handle(it item) {
	closeDone := true
	defer func() {
		if r := recover(); r != nil {
			c.diag(fmt.Sprintf("collector step failed: %v", r))
		}
		if closeDone && it.done != nil {
			close(it.done)
		}
	}()
	if c.stepHook != nil {
		c.stepHook()
	}
	switch {
	case it.events != nil:
		c.process(it.events)
	case it.sub != nil:
		if !c.greet(it.sub) {
			closeDone = false // stopped: Subscribe greets on its caller's goroutine
			return
		}
		c.subs = append(c.subs, it.sub)
	case it.run != nil:
		it.run()
	}
}

func (c *Collector) process(events []wire.SourceEvent) {
	sawAgent := false
	for _, e := range events {
		if c.isStopped() {
			return
		}
		if !c.opts.Demo {
			e = c.enrich(e)
			if c.isStopped() {
				return
			}
		}
		if _, ok := e.Ev.(wire.AgentUpsert); ok {
			sawAgent = true
		}
		if out, ok := c.apply(e); ok {
			c.deliver(out)
		}
	}
	if sawAgent {
		c.syncRoots()
	}
}

// apply is State.Apply; an event that makes it panic is skipped.
func (c *Collector) apply(e wire.SourceEvent) (out wire.SourceEvent, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return c.state.Apply(e)
}

// tick ends timed-out calls. Its events are final: they go out directly and
// are not applied to the state again.
func (c *Collector) tick() {
	for _, e := range c.state.Tick(wire.NowMs()) {
		c.deliver(e)
	}
	c.syncRoots()
}

// syncRoots watches the --watch directories plus the project roots of
// present agents.
func (c *Collector) syncRoots() {
	c.srcMu.Lock()
	fs := c.fs
	c.srcMu.Unlock()
	if fs == nil || c.isStopped() {
		return
	}
	var roots []string
	for _, r := range slices.Concat(c.opts.Watch, c.state.WatchRoots(wire.NowMs(), maxWatchRoots)) {
		if !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			c.diag(fmt.Sprintf("cannot update watch roots: %v", r))
		}
	}()
	fs.SetRoots(roots[:min(len(roots), maxWatchRoots)])
}

// deliver wraps e in the next live envelope and sends it to every
// subscriber.
func (c *Collector) deliver(e wire.SourceEvent) {
	if c.isStopped() {
		return
	}
	c.seq++
	env := wire.Envelope{V: wire.SchemaVersion, Host: c.opts.Host, Seq: c.seq, Ts: e.Ts, Ev: e.Ev}
	c.subs = slices.DeleteFunc(c.subs, func(s *subscriber) bool { return s.removed.Load() })
	for _, s := range c.subs {
		if !s.removed.Load() {
			s.send(env)
		}
	}
}

// diag reports a diagnostic message to Options.Diag, or to os.Stderr when it
// is nil.
func (c *Collector) diag(msg string) {
	if c.opts.Diag == nil {
		fmt.Fprintf(os.Stderr, "agent-view: %s\n", msg)
		return
	}
	defer func() { _ = recover() }()
	c.opts.Diag(msg)
}

// greet sends hello and snapshot to s. It reports false once the
// collector is stopped.
func (c *Collector) greet(s *subscriber) bool {
	if c.isStopped() {
		return false
	}
	hello, snap := c.greeting()
	s.send(hello)
	s.send(snap)
	return true
}

// greeting returns the hello and snapshot envelopes. They carry the
// current sequence number. A Snapshot that panics gives an empty snapshot
// (see snapshot), so a subscriber always gets both.
func (c *Collector) greeting() (hello, snap wire.Envelope) {
	now := wire.NowMs()
	ts := wire.FormatISO(now)
	hello = wire.Envelope{V: wire.SchemaVersion, Host: c.opts.Host, Seq: c.seq, Ts: ts, Ev: wire.Hello{
		CollectorVersion: wire.CollectorVersion, Platform: wire.Platform(), Now: ts, Demo: c.opts.Demo,
	}}
	snap = wire.Envelope{V: wire.SchemaVersion, Host: c.opts.Host, Seq: c.seq, Ts: ts, Ev: c.snapshot(now)}
	return hello, snap
}

// snapshot is State.Snapshot. A panic in it is reported and gives an empty
// snapshot.
func (c *Collector) snapshot(now int64) (snap wire.Snapshot) {
	defer func() {
		if r := recover(); r != nil {
			c.diag(fmt.Sprintf("snapshot failed: %v", r))
			snap = wire.Snapshot{}
		}
	}()
	return c.state.Snapshot(now)
}

// Enrichment

// enrich resolves the project root of an agent and the paths of a call to
// real paths. An event that makes it panic passes unchanged.
func (c *Collector) enrich(e wire.SourceEvent) (out wire.SourceEvent) {
	defer func() {
		if recover() != nil {
			out = e
		}
	}()
	switch ev := e.Ev.(type) {
	case wire.AgentUpsert:
		a := ev.Agent
		if a.Cwd == nil || *a.Cwd == "" || a.ProjectRoot != nil {
			return e
		}
		root, ok := c.paths.projectRootFor(*a.Cwd)
		if !ok {
			return e
		}
		a.ProjectRoot = &root
		return wire.SourceEvent{Ts: e.Ts, Ev: wire.AgentUpsert{Agent: a}}
	case wire.ToolStart:
		if len(ev.Call.Files) == 0 {
			return e
		}
		ev.Call.Files = c.paths.mapPaths(ev.Call.Files)
		return wire.SourceEvent{Ts: e.Ts, Ev: ev}
	case wire.ToolUpdate:
		if len(ev.Files) == 0 {
			return e
		}
		ev.Files = c.paths.mapPaths(ev.Files)
		return wire.SourceEvent{Ts: e.Ts, Ev: ev}
	case wire.ToolEnd:
		if len(ev.Writes) == 0 {
			return e
		}
		writes := make([]wire.Write, len(ev.Writes))
		for i, w := range ev.Writes {
			writes[i] = wire.Write{Path: c.paths.realPath(w.Path), Op: w.Op}
		}
		ev.Writes = writes
		return wire.SourceEvent{Ts: e.Ts, Ev: ev}
	}
	return e
}
