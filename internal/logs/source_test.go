package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-view/internal/watch"
	"agent-view/internal/wire"
)

// srcDeadline bounds every wait. FSEvents can lag by seconds when fseventsd
// is busy (see the Task 7 report); a healthy run returns in milliseconds.
const srcDeadline = 10 * time.Second

func toolLine(id string) string {
	return fmt.Sprintf(`{"sessionId":"root","cwd":"/project","timestamp":"2026-01-01T00:00:01.000Z","type":"assistant",`+
		`"message":{"content":[{"type":"tool_use","id":%q,"name":"Read","input":{"file_path":"/project/a.ts"}}]}}`+"\n", id)
}

// fakeWatcher is a watcher that reports nothing.
type fakeWatcher struct {
	degraded string
	partial  bool
	onClose  func()
}

func (w fakeWatcher) Close() {
	if w.onClose != nil {
		w.onClose()
	}
}

func (w fakeWatcher) Degraded() string { return w.degraded }

func (w fakeWatcher) Partial() bool { return w.partial }

func noWatch(string, func(string, watch.Op), watch.Options) watch.Watcher { return fakeWatcher{} }

type srcStatus struct {
	source string
	state  wire.SourceState
}

// srcRig wires a Source to recorders. Its callbacks run on the source's
// goroutine, so every read goes through the lock.
type srcRig struct {
	src      *Source
	mu       sync.Mutex
	events   []wire.SourceEvent
	statuses []srcStatus
	warnings []string
}

// newSrcRig starts from the TypeScript harness: host mac, poll 50 ms,
// rescan 100 ms, and unless a test says otherwise a safety rescan as fast
// with healthy watchers as without.
func newSrcRig(t *testing.T, roots []Root, o SourceOptions) *srcRig {
	t.Helper()
	r := &srcRig{}
	o.Host = "mac"
	o.Roots = roots
	if o.PollMs == 0 {
		o.PollMs = 50
	}
	if o.RescanMs == 0 {
		o.RescanMs = 100
	}
	if o.HealthyRescanMs == 0 {
		o.HealthyRescanMs = o.RescanMs
	}
	o.Emit = func(batch []wire.SourceEvent) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, batch...)
	}
	o.OnStatus = func(source string, s wire.SourceState) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.statuses = append(r.statuses, srcStatus{source, s})
	}
	o.OnWarning = func(msg string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.warnings = append(r.warnings, msg)
	}
	r.src = NewSource(o)
	t.Cleanup(r.src.Stop)
	return r
}

func (r *srcRig) start(t *testing.T) {
	t.Helper()
	if err := r.src.Start(); err != nil {
		t.Fatal(err)
	}
}

func (r *srcRig) Events() []wire.SourceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *srcRig) Statuses() []srcStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.statuses)
}

func (r *srcRig) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.warnings)
}

func (r *srcRig) toolStarts() []string {
	var ids []string
	for _, s := range only[wire.ToolStart](r.Events()) {
		ids = append(ids, s.Call.CallID)
	}
	return ids
}

func (r *srcRig) hasStart(id string) func() bool {
	return func() bool { return slices.Contains(r.toolStarts(), id) }
}

func (r *srcRig) statusesOf(source string) []srcStatus {
	var out []srcStatus
	for _, s := range r.Statuses() {
		if s.source == source {
			out = append(out, s)
		}
	}
	return out
}

// until waits for ok, polling every 5 ms up to srcDeadline.
func until(t *testing.T, ok func() bool, what string) {
	t.Helper()
	end := time.Now().Add(srcDeadline)
	for !ok() {
		if time.Now().After(end) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func claudeRoot(dir string) Root {
	return Root{Provider: "claude", Path: filepath.Join(dir, "projects"), Scope: "default"}
}

// writeLog creates file and its directory with text.
func writeLog(t *testing.T, file, text string) {
	t.Helper()
	mkdir(t, filepath.Dir(file))
	write(t, file, []byte(text))
}

// running reports whether the source's goroutine, which owns the timers, is
// still running.
func running(s *Source) bool {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// LogSource emits Claude events within 1 second of an append (fs.watch path)
func TestLogSourceWatchLatency(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	log := filepath.Join(root.Path, "proj", "main.jsonl")
	writeLog(t, log, toolLine("t0"))
	// The poll is far slower than the budget, so only the watcher can meet it.
	r := newSrcRig(t, []Root{root}, SourceOptions{PollMs: 60_000, RescanMs: 60_000})
	r.start(t)
	eq(t, r.toolStarts(), []string{"t0"})
	begin := time.Now()
	appendTo(t, log, []byte(toolLine("t1")))
	// FSEvents can lag by seconds under load, so the wait allows srcDeadline
	// and logs the latency instead of failing at 1 s.
	until(t, r.hasStart("t1"), "tool.start t1")
	t.Logf("tool.start t1 after %v", time.Since(begin))
	for _, s := range only[wire.ToolStart](r.Events()) {
		if s.Call.CallID != "t1" {
			continue
		}
		eq(t, s.Call.AgentID, "mac/claude:default:root")
		eq(t, s.Call.Category, "read")
		eq(t, s.Call.Files, []string{"/project/a.ts"})
	}
}

// LogSource safety poll picks up appends when watch callbacks are suppressed
func TestLogSourcePollWithoutWatcher(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	log := filepath.Join(root.Path, "proj", "main.jsonl")
	writeLog(t, log, toolLine("t0"))
	r := newSrcRig(t, []Root{root}, SourceOptions{Watch: noWatch, RescanMs: 60_000})
	r.start(t)
	appendTo(t, log, []byte(toolLine("t1")))
	until(t, r.hasStart("t1"), "poll to pick up t1")
}

// LogSource finds a new log file after a rescan, with the watcher suppressed and with it active
func TestLogSourceFindsNewLog(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	mkdir(t, root.Path, "proj")
	polled := newSrcRig(t, []Root{root}, SourceOptions{Watch: noWatch})
	polled.start(t)
	write(t, filepath.Join(root.Path, "proj", "new.jsonl"), []byte(toolLine("n1")))
	until(t, polled.hasStart("n1"), "rescan to find n1")
	polled.src.Stop()

	// With the watcher active and a slow rescan, the new file arrives through the watcher-triggered rescan.
	watched := newSrcRig(t, []Root{root}, SourceOptions{PollMs: 60_000, RescanMs: 60_000})
	watched.start(t)
	writeLog(t, filepath.Join(root.Path, "other", "new2.jsonl"), toolLine("n2"))
	until(t, watched.hasStart("n2"), "watcher-triggered rescan to find n2")
}

// I5: with healthy watchers the safety rescan is slow; a degraded watcher brings back the fast rescan
func TestLogSourceHealthyRescanSlow(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	mkdir(t, root.Path, "proj")
	// The watcher looks healthy but reports nothing, so only a periodic rescan can find a new file.
	healthy := newSrcRig(t, []Root{root}, SourceOptions{Watch: noWatch, RescanMs: 50, HealthyRescanMs: 60_000})
	healthy.start(t)
	write(t, filepath.Join(root.Path, "proj", "quiet.jsonl"), []byte(toolLine("q1")))
	time.Sleep(300 * time.Millisecond) // 6x the degraded interval
	eq(t, healthy.toolStarts(), []string(nil))
	healthy.src.Stop()

	degradedWatch := func(string, func(string, watch.Op), watch.Options) watch.Watcher {
		return fakeWatcher{degraded: "watcher error"}
	}
	degraded := newSrcRig(t, []Root{root}, SourceOptions{Watch: degradedWatch, RescanMs: 50, HealthyRescanMs: 60_000})
	degraded.start(t)
	write(t, filepath.Join(root.Path, "proj", "loud.jsonl"), []byte(toolLine("l1")))
	until(t, degraded.hasStart("l1"), "fast rescan to find l1")
}

// A partial watcher (a capacity limit) is kept across scans and reported
// once. A degraded watcher that is not partial is replaced on each scan.
func TestLogSourceKeepsPartialWatcher(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	mkdir(t, root.Path, "proj")
	key := "logs:" + root.Path
	capped := "more than 12000 directories; later directories are not watched"
	partial := &watchTracker{degraded: capped, partial: true}
	r := newSrcRig(t, []Root{root}, SourceOptions{Watch: partial.watch, RescanMs: 50, HealthyRescanMs: 60_000})
	r.start(t)
	// Each new log is found by a later rescan, so at least two scans ran.
	for _, id := range []string{"p1", "p2"} {
		write(t, filepath.Join(root.Path, "proj", id+".jsonl"), []byte(toolLine(id)))
		until(t, r.hasStart(id), "rescan to find "+id)
	}
	eq(t, [2]int32{partial.opened.Load(), partial.closed.Load()}, [2]int32{1, 0})
	eq(t, r.statusesOf(key), []srcStatus{{key, wire.SourceState{State: "degraded", Detail: capped}}})
	r.src.Stop()

	failed := &watchTracker{degraded: "watcher error on root: ENOENT"}
	r = newSrcRig(t, []Root{root}, SourceOptions{Watch: failed.watch, RescanMs: 50, HealthyRescanMs: 60_000})
	r.start(t)
	until(t, func() bool { return failed.opened.Load() >= 3 }, "the failed watcher to be replaced twice")
	if got := failed.closed.Load(); got < 2 {
		t.Errorf("closed %d watchers, want at least 2", got)
	}
}

// A watcher hint that does not fit in the buffer is dropped, and the next
// poll starts a rescan: a new log behind a dropped hint is found without
// waiting for the safety rescan.
func TestLogSourceDroppedHintRescans(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	main := filepath.Join(root.Path, "proj", "main.jsonl")
	writeLog(t, main, toolLine("t0"))
	// The first scan opens the watcher before Start returns.
	var hint func(string, watch.Op)
	watchFn := func(_ string, cb func(string, watch.Op), _ watch.Options) watch.Watcher {
		hint = cb
		return fakeWatcher{}
	}
	var mu sync.Mutex
	var ids []string
	var hold atomic.Bool
	held, release := make(chan struct{}), make(chan struct{})
	src := NewSource(SourceOptions{
		Host: "mac", Roots: []Root{root}, PollMs: 50, RescanMs: 60_000, HealthyRescanMs: 60_000, Watch: watchFn,
		Emit: func(b []wire.SourceEvent) {
			if hold.CompareAndSwap(true, false) {
				close(held)
				<-release
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range only[wire.ToolStart](b) {
				ids = append(ids, s.Call.CallID)
			}
		},
		OnStatus: func(string, wire.SourceState) {},
	})
	t.Cleanup(src.Stop)
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}
	// Hold the source goroutine in Emit (the poll reads t1), so the hints
	// fill the buffer and the hint for the new log is dropped.
	hold.Store(true)
	appendTo(t, main, []byte(toolLine("t1")))
	select {
	case <-held:
	case <-time.After(srcDeadline):
		close(release)
		t.Fatal("the poll did not read t1")
	}
	writeLog(t, filepath.Join(root.Path, "proj", "new.jsonl"), toolLine("n1"))
	for range hintBuffer {
		hint(filepath.Join("proj", "main.jsonl"), watch.Change)
	}
	hint(filepath.Join("proj", "new.jsonl"), watch.Rename)
	close(release)
	until(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(ids, "n1")
	}, "a rescan to find n1")
}

// LogSource never reads one file twice at once: a burst of appends emits each record once
func TestLogSourceNoConcurrentReads(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	log := filepath.Join(root.Path, "proj", "main.jsonl")
	writeLog(t, log, toolLine("t0"))
	r := newSrcRig(t, []Root{root}, SourceOptions{PollMs: 1, RescanMs: 60_000})
	r.start(t)
	const total = 150
	for i := 1; i <= total; i++ {
		appendTo(t, log, []byte(toolLine(fmt.Sprintf("b%d", i))))
		if i%10 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	until(t, r.hasStart(fmt.Sprintf("b%d", total)), "last burst record")
	time.Sleep(100 * time.Millisecond)
	ids := r.toolStarts()
	eq(t, len(ids), total+1)
	unique := map[string]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	eq(t, len(unique), total+1)
}

// LogSource picks the adapter from the root provider
func TestLogSourceAdapterByProvider(t *testing.T) {
	dir := realDir(t)
	root := Root{Provider: "codex", Path: filepath.Join(dir, "sessions"), Scope: "work"}
	log := filepath.Join(root.Path, "2026", "rollout.jsonl")
	writeLog(t, log, `{"type":"session_meta","timestamp":"2026-01-01T00:00:01.000Z","payload":{"id":"main","cwd":"/project"}}`+"\n")
	r := newSrcRig(t, []Root{root}, SourceOptions{})
	r.start(t)
	upserts := only[wire.AgentUpsert](r.Events())
	if len(upserts) == 0 {
		t.Fatal("no agent.upsert")
	}
	eq(t, upserts[0].Agent.ID, "mac/codex:work:main")
	eq(t, upserts[0].Agent.Provider, "codex")
}

// LogSource forgets a file that drops out of the selection
func TestLogSourceForgetsDroppedFile(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	log := filepath.Join(root.Path, "proj", "main.jsonl")
	writeLog(t, log, toolLine("t0"))
	r := newSrcRig(t, []Root{root}, SourceOptions{SinceMs: 60_000, Watch: noWatch})
	r.start(t)
	cov := r.src.Coverage()
	eq(t, [2]int{cov.LogsOpen, cov.LogsDiscovered}, [2]int{1, 1})
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(log, old, old); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return r.src.Coverage().LogsOpen == 0 }, "file to drop out")
	cov = r.src.Coverage()
	eq(t, [2]int{cov.LogsOpen, cov.LogsDiscovered}, [2]int{0, 0})
}

// LogSource coverage lists roots and counts malformed records
func TestLogSourceCoverage(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	log := filepath.Join(root.Path, "proj", "main.jsonl")
	writeLog(t, log, "not json\n"+toolLine("t0"))
	r := newSrcRig(t, []Root{root}, SourceOptions{})
	r.start(t)
	eq(t, r.src.Coverage(), LogCoverage{LogRoots: []string{root.Path}, LogsOpen: 1, LogsDiscovered: 1, MalformedRecords: 1})
}

// LogSource reports source.status degraded for an unreadable root and keeps running
func TestLogSourceUnreadableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := realDir(t)
	bad := Root{Provider: "codex", Path: filepath.Join(dir, "locked"), Scope: "default"}
	good := claudeRoot(dir)
	missing := Root{Provider: "codex", Path: filepath.Join(dir, "missing"), Scope: "x"}
	log := filepath.Join(good.Path, "proj", "main.jsonl")
	mkdir(t, bad.Path)
	writeLog(t, log, toolLine("t0"))
	if err := os.Chmod(bad.Path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad.Path, 0o755) })
	r := newSrcRig(t, []Root{bad, good, missing}, SourceOptions{})
	r.start(t)
	key := "logs:" + bad.Path
	eq(t, r.statusesOf(key), []srcStatus{{key, wire.SourceState{State: "degraded", Detail: "EACCES"}}})
	eq(t, r.statusesOf("logs:"+missing.Path), []srcStatus(nil))
	// The healthy root keeps working.
	eq(t, r.toolStarts(), []string{"t0"})
	appendTo(t, log, []byte(toolLine("t1")))
	until(t, r.hasStart("t1"), "healthy root append")
	// The root recovers.
	if err := os.Chmod(bad.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool {
		return slices.ContainsFunc(r.statusesOf(key), func(s srcStatus) bool { return s.state.State == "ok" })
	}, "root to recover")
	var states []string
	for _, s := range r.statusesOf(key) {
		states = append(states, s.state.State)
	}
	eq(t, states, []string{"degraded", "ok"})
}

// watchTracker counts the watchers a fake Watch opened and closed, so a test
// can prove Stop released them. Its watchers report degraded and partial.
type watchTracker struct {
	opened, closed atomic.Int32
	degraded       string
	partial        bool
}

func (w *watchTracker) watch(string, func(string, watch.Op), watch.Options) watch.Watcher {
	w.opened.Add(1)
	return fakeWatcher{degraded: w.degraded, partial: w.partial, onClose: func() { w.closed.Add(1) }}
}

func (w *watchTracker) live() int32 { return w.opened.Load() - w.closed.Load() }

// stop() during start() leaves no timers and no watchers, and emits nothing afterwards
func TestLogSourceStopDuringStart(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	for _, n := range []string{"a", "b", "c"} {
		writeLog(t, filepath.Join(root.Path, "proj", n+".jsonl"), toolLine("t-"+n))
	}

	// Stop while the initial scan is still running. The fake watcher opens
	// during the scan and holds it until Stop, called from this goroutine,
	// has taken effect.
	res := &watchTracker{}
	var early *srcRig
	inScan := make(chan struct{})
	early = newSrcRig(t, []Root{root}, SourceOptions{Watch: func(p string, cb func(string, watch.Op), o watch.Options) watch.Watcher {
		w := res.watch(p, cb, o)
		close(inScan)
		for !early.src.isStopped() {
			time.Sleep(time.Millisecond)
		}
		return w
	}})
	started := make(chan error, 1)
	go func() { started <- early.src.Start() }()
	<-inScan
	early.src.Stop()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	eq(t, early.Events(), []wire.SourceEvent(nil))
	eq(t, early.Statuses(), []srcStatus(nil))
	eq(t, running(early.src), false)
	eq(t, res.live(), int32(0))

	// Stop while the initial reads are running: the first batch is the last.
	res = &watchTracker{}
	var source *Source
	var mu sync.Mutex
	var batches [][]wire.SourceEvent
	source = NewSource(SourceOptions{
		Host: "mac", Roots: []Root{root}, PollMs: 50, RescanMs: 100, Watch: res.watch,
		Emit: func(b []wire.SourceEvent) {
			mu.Lock()
			batches = append(batches, b)
			mu.Unlock()
			source.Stop()
		},
		OnStatus: func(string, wire.SourceState) {},
	})
	t.Cleanup(source.Stop)
	if err := source.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	eq(t, len(batches), 1)
	mu.Unlock()
	eq(t, running(source), false)
	eq(t, res.live(), int32(0))
}

// A selected log deleted between the scan and its read gives no warning and
// no events, and the source keeps running (Review Focus 4).
func TestLogSourceDeletedBeforeRead(t *testing.T) {
	dir := realDir(t)
	root := claudeRoot(dir)
	gone := filepath.Join(root.Path, "proj", "gone.jsonl")
	writeLog(t, gone, toolLine("t0"))
	// The scan opens the watcher after it selects the logs and before it
	// reads them: the fake deletes the selected log in that gap.
	deleteFirst := func(string, func(string, watch.Op), watch.Options) watch.Watcher {
		if err := os.Remove(gone); err != nil {
			t.Error(err)
		}
		return fakeWatcher{}
	}
	r := newSrcRig(t, []Root{root}, SourceOptions{Watch: deleteFirst})
	r.start(t)
	eq(t, r.Events(), []wire.SourceEvent(nil))
	eq(t, r.src.Coverage().LogsDiscovered, 1)
	// The source keeps running: a later log arrives through the rescan.
	write(t, filepath.Join(root.Path, "proj", "later.jsonl"), []byte(toolLine("t1")))
	until(t, r.hasStart("t1"), "rescan to find t1")
	eq(t, r.toolStarts(), []string{"t1"})
	eq(t, r.Warnings(), []string(nil))
}
