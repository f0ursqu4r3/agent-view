package logs

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-view/internal/fsutil"
	"agent-view/internal/watch"
	"agent-view/internal/wire"
)

const (
	defaultMaxLogs         = 120
	defaultSinceMs         = 24 * 3600 * 1000
	defaultPollMs          = 2_000
	defaultRescanMs        = 10_000
	defaultHealthyRescanMs = 120_000
	// rescanDebounce is the quiet time after a watcher hint before a rescan,
	// so a burst of new files costs one scan.
	rescanDebounce = 50 * time.Millisecond
	// hintBuffer is how many watcher hints wait for the source goroutine. A
	// hint that does not fit is dropped: the poll covers it.
	hintBuffer = 256
)

// SourceOptions configure a Source. Zero values take the defaults: 120
// logs, 24 hours, a 2,000 ms poll, a 10,000 ms safety rescan while a
// watcher is missing or degraded, and 120,000 ms while every root has a
// healthy watcher.
type SourceOptions struct {
	Host  string
	Roots []Root
	// Emit gets the events of one read of one file. OnStatus gets each
	// change of a root's health, under the source key "logs:<root>".
	// OnWarning (optional) gets warnings. All three run on the source's
	// goroutine; they may call Stop. A panic in Emit becomes the warning
	// "Event consumer failed: <message>".
	Emit            func([]wire.SourceEvent)
	OnStatus        func(source string, s wire.SourceState)
	MaxLogs         int
	SinceMs         int64
	PollMs          int64
	RescanMs        int64
	HealthyRescanMs int64
	OnWarning       func(string)
	// Watch starts a tree watcher; nil means watch.Tree. Tests replace it.
	Watch func(root string, onChange func(string, watch.Op), o watch.Options) watch.Watcher
}

// LogCoverage is the log part of the collector's coverage.
type LogCoverage struct {
	LogRoots                                   []string
	LogsOpen, LogsDiscovered, MalformedRecords int
}

// Source tails the newest Claude and Codex logs under the given roots and
// emits adapter events. It replaces src/collector/sources/log-source.ts.
// File watchers give low latency, a poll of the selected files covers missed
// callbacks, and a rescan finds new logs. A watcher hint for an unknown log
// starts a rescan at once. The safety rescan walks every root, which is
// costly on large log trees: it runs every 120 s while every root has a
// healthy watcher, and every 10 s otherwise.
//
// One goroutine owns the selection, the tailer, the watchers and the
// timers, so a file is never read twice at once. Watcher callbacks only
// pass a hint to it, and never block.
type Source struct {
	roots         []Root
	emit          func([]wire.SourceEvent)
	onStatus      func(string, wire.SourceState)
	onWarning     func(string)
	watch         func(string, func(string, watch.Op), watch.Options) watch.Watcher
	maxLogs       int
	sinceMs       int64
	poll          time.Duration
	rescanEvery   time.Duration
	healthyRescan time.Duration
	hints         chan string
	quit          chan struct{}
	done          chan struct{} // closed when the source goroutine ends
	// The coverage counts, written by the source goroutine.
	open, malformed, discovered atomic.Int64

	// mu guards the fields below. It is never held while a consumer
	// callback runs.
	mu      sync.Mutex
	started bool
	stopped bool
	busy    bool // a consumer callback runs

	// Owned by the source goroutine.
	claude   *ClaudeAdapter
	codex    *CodexAdapter
	tailer   *Tailer
	selected map[string]RecentLog
	order    []string // the keys of selected, in insertion order
	watchers map[string]watch.Watcher
	reported map[string]string
	batch    []wire.SourceEvent
	lastScan time.Time
}

// NewSource returns a Source that does nothing until Start.
func NewSource(o SourceOptions) *Source {
	s := &Source{
		roots:         slices.Clone(o.Roots),
		emit:          o.Emit,
		onStatus:      o.OnStatus,
		onWarning:     o.OnWarning,
		watch:         o.Watch,
		maxLogs:       o.MaxLogs,
		sinceMs:       o.SinceMs,
		poll:          msOr(o.PollMs, defaultPollMs),
		rescanEvery:   msOr(o.RescanMs, defaultRescanMs),
		healthyRescan: msOr(o.HealthyRescanMs, defaultHealthyRescanMs),
		hints:         make(chan string, hintBuffer),
		quit:          make(chan struct{}),
		done:          make(chan struct{}),
		claude:        NewClaudeAdapter(o.Host),
		codex:         NewCodexAdapter(o.Host),
		selected:      map[string]RecentLog{},
		watchers:      map[string]watch.Watcher{},
		reported:      map[string]string{},
	}
	if s.maxLogs <= 0 {
		s.maxLogs = defaultMaxLogs
	}
	if s.sinceMs <= 0 {
		s.sinceMs = defaultSinceMs
	}
	if s.watch == nil {
		s.watch = watch.Tree
	}
	if s.emit == nil {
		s.emit = func([]wire.SourceEvent) {}
	}
	if s.onStatus == nil {
		s.onStatus = func(string, wire.SourceState) {}
	}
	s.tailer = NewTailer(s.ingest, 0, s.warn)
	return s
}

func msOr(ms, def int64) time.Duration {
	if ms <= 0 {
		ms = def
	}
	return time.Duration(ms) * time.Millisecond
}

// Start runs the first scan, reads the logs it selected (oldest first) and
// emits their events, then starts the poll and the safety rescan. It
// returns once the first scan is done, or once Stop has ended the source.
// A second call, or a call after Stop, does nothing. The error is always
// nil.
func (s *Source) Start() error {
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()
	ready := make(chan struct{})
	go s.run(ready)
	select {
	case <-ready:
	case <-s.done:
	}
	return nil
}

// Stop ends the source: no consumer callback starts after it returns. When
// no consumer callback runs, Stop waits until the watchers are closed and
// the timers stopped. While one runs (it may be the caller), Stop returns at
// once and the source ends after that callback returns. Stop is idempotent.
func (s *Source) Stop() {
	s.mu.Lock()
	first := !s.stopped
	s.stopped = true
	wait := s.started && !s.busy
	s.mu.Unlock()
	if first {
		close(s.quit)
	}
	if wait {
		<-s.done
	}
}

// Coverage is safe from any goroutine.
func (s *Source) Coverage() LogCoverage {
	roots := make([]string, 0, len(s.roots))
	for _, r := range s.roots {
		roots = append(roots, r.Path)
	}
	return LogCoverage{
		LogRoots:         roots,
		LogsOpen:         int(s.open.Load()),
		LogsDiscovered:   int(s.discovered.Load()),
		MalformedRecords: int(s.malformed.Load()),
	}
}

func (s *Source) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// call runs a consumer callback unless the source is stopped. A panic
// passes through to the caller.
func (s *Source) call(fn func()) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.busy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
	fn()
}

func (s *Source) warn(msg string) {
	if s.onWarning == nil {
		return
	}
	// A failing warning consumer has nowhere to report.
	defer func() { _ = recover() }()
	s.call(func() { s.onWarning(msg) })
}

// run is the source goroutine.
func (s *Source) run(ready chan<- struct{}) {
	defer close(s.done)
	defer s.closeWatchers()
	s.rescan()
	if s.isStopped() {
		return
	}
	poll := time.NewTicker(s.poll)
	defer poll.Stop()
	check := time.NewTicker(s.checkEvery())
	defer check.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	close(ready)
	for {
		select {
		case <-s.quit:
			return
		case <-poll.C:
			for _, file := range s.order {
				s.readFile(file)
			}
		case <-check.C:
			s.safetyRescan()
		case <-debounceC:
			debounceC = nil
			s.rescan()
		case file := <-s.hints:
			// A trigger during a read gives one follow-up read: take every
			// waiting hint, once each.
			files := []string{file}
		drain:
			for {
				select {
				case f := <-s.hints:
					if !slices.Contains(files, f) {
						files = append(files, f)
					}
				default:
					break drain
				}
			}
			for _, f := range files {
				if _, ok := s.selected[f]; ok {
					s.readFile(f)
				} else if debounceC == nil {
					debounce = time.NewTimer(rescanDebounce)
					debounceC = debounce.C
				}
			}
		}
	}
}

func (s *Source) closeWatchers() {
	for path, w := range s.watchers {
		w.Close()
		delete(s.watchers, path)
	}
}

func (s *Source) ingest(record any, file string) {
	log, ok := s.selected[file]
	if !ok {
		return
	}
	var events []wire.SourceEvent
	if log.Provider == "claude" {
		events = s.claude.Ingest(record, file, log.Scope)
	} else {
		events = s.codex.Ingest(record, file, log.Scope)
	}
	s.batch = append(s.batch, events...)
}

func (s *Source) flush() {
	if len(s.batch) == 0 {
		return
	}
	events := s.batch
	s.batch = nil
	defer func() {
		if r := recover(); r != nil {
			s.warn(fmt.Sprintf("Event consumer failed: %v", r))
		}
	}()
	s.call(func() { s.emit(events) })
}

// readFile reads one file and emits its new events as one batch. A missing
// file is silent.
func (s *Source) readFile(file string) {
	if s.isStopped() {
		return
	}
	if err := s.read(file); err != nil {
		if code := fsutil.ErrCode(err); code != "ENOENT" {
			s.warn("Cannot read " + file + ": " + code)
		}
	}
	s.syncCoverage()
	s.flush()
}

// read is Tailer.Read; a panic while reading becomes an error, as a throw
// does in TypeScript.
func (s *Source) read(file string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return s.tailer.Read(file)
}

func (s *Source) syncCoverage() {
	s.open.Store(int64(s.tailer.OpenFiles()))
	s.malformed.Store(int64(s.tailer.Malformed()))
}

// checkEvery is how often the safety rescan checks whether a rescan is due.
func (s *Source) checkEvery() time.Duration { return min(s.rescanEvery, s.healthyRescan) }

// watchersHealthy is true when every root has a watcher that reports no fault.
func (s *Source) watchersHealthy() bool {
	for _, r := range s.roots {
		w := s.watchers[r.Path]
		if w == nil || w.Degraded() != "" {
			return false
		}
	}
	return true
}

// safetyRescan rescans when the interval for the current watcher health
// has passed since the last scan of any kind.
func (s *Source) safetyRescan() {
	due := s.rescanEvery
	if s.watchersHealthy() {
		due = s.healthyRescan
	}
	// Half a check of slack, so timer jitter never pushes a due rescan to the next check.
	if time.Since(s.lastScan)+s.checkEvery()/2 >= due {
		s.rescan()
	}
}

func (s *Source) rescan() {
	if s.isStopped() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.warn(fmt.Sprintf("Log scan failed: %v", r))
		}
	}()
	s.scan()
}

func (s *Source) scan() {
	s.lastScan = time.Now()
	codes := map[string]string{}
	all := ScanLogs(s.roots, s.sinceMs, func(r Root, code string) { codes[r.Path] = code })
	if s.isStopped() {
		return
	}
	s.discovered.Store(int64(len(all)))
	chosen := SelectLogs(all, s.maxLogs)
	keep := make(map[string]bool, len(chosen))
	for _, l := range chosen {
		keep[l.File] = true
	}
	kept := s.order[:0]
	for _, file := range s.order {
		if keep[file] {
			kept = append(kept, file)
			continue
		}
		delete(s.selected, file)
		s.tailer.Forget(file)
	}
	s.order = kept
	s.syncCoverage()
	var fresh []RecentLog
	for _, l := range chosen {
		if _, ok := s.selected[l.File]; !ok {
			fresh = append(fresh, l)
			s.order = append(s.order, l.File)
		}
		s.selected[l.File] = l
	}
	for _, root := range s.roots {
		if code, ok := codes[root.Path]; ok {
			s.ensureWatcher(root, code)
		}
	}
	// Oldest first, so the newest activity is the last thing emitted.
	for i := len(fresh) - 1; i >= 0; i-- {
		if s.isStopped() {
			return
		}
		s.readFile(fresh[i].File)
	}
}

// ensureWatcher watches a readable root (code "") and reports the root's
// status when it changes.
func (s *Source) ensureWatcher(root Root, code string) {
	w := s.watchers[root.Path]
	if code == "" && w != nil && w.Degraded() != "" {
		w.Close()
		delete(s.watchers, root.Path)
		w = nil
	}
	if code == "" && w == nil {
		w = s.watch(root.Path, func(rel string, _ watch.Op) {
			if !strings.HasSuffix(rel, ".jsonl") {
				return
			}
			select {
			case s.hints <- filepath.Join(root.Path, rel):
			default:
			}
		}, watch.Options{})
		s.watchers[root.Path] = w
	}
	detail := code
	if detail == "" && w != nil {
		detail = w.Degraded()
	}
	state := wire.SourceState{State: "ok"}
	if detail != "" {
		state = wire.SourceState{State: "degraded", Detail: detail}
	}
	key := state.State + ":" + state.Detail
	source := "logs:" + root.Path
	if s.reported[source] == key {
		return
	}
	s.reported[source] = key
	s.call(func() { s.onStatus(source, state) })
}
