package fschange

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-view/internal/fsutil"
	"agent-view/internal/watch"
	"agent-view/internal/wire"
)

// deadline bounds every wait. FSEvents can lag by seconds when fseventsd is
// busy (see the Task 7 report); a healthy run returns in milliseconds.
const deadline = 10 * time.Second

// tempDir is a fresh directory with its symlinks resolved, as the
// TypeScript tests use fs.realpath(mkdtemp(...)).
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type statusRec struct {
	source string
	s      wire.SourceState
}

// harness records what a Source emits. Its callbacks run on watcher
// goroutines, so every read goes through the lock.
type harness struct {
	src      *Source
	mu       sync.Mutex
	events   []wire.SourceEvent
	statuses []statusRec
}

func newHarness(t *testing.T, o Options) *harness {
	t.Helper()
	h := &harness{}
	o.Emit = func(batch []wire.SourceEvent) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.events = append(h.events, batch...)
	}
	o.OnStatus = func(source string, s wire.SourceState) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.statuses = append(h.statuses, statusRec{source, s})
	}
	h.src = New(o)
	t.Cleanup(h.src.Stop)
	return h
}

func (h *harness) Events() []wire.SourceEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}

func (h *harness) Statuses() []statusRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.statuses)
}

// statusesOf returns the statuses of source in the given state.
func (h *harness) statusesOf(source, state string) []statusRec {
	var out []statusRec
	for _, x := range h.Statuses() {
		if x.source == source && x.s.State == state {
			out = append(out, x)
		}
	}
	return out
}

func (h *harness) has(match func(wire.FsChange) bool) bool {
	return slices.ContainsFunc(h.Events(), func(e wire.SourceEvent) bool {
		c, ok := e.Ev.(wire.FsChange)
		return ok && match(c)
	})
}

// waitEvent waits until an event matches, as the TypeScript wait() does.
func (h *harness) waitEvent(t *testing.T, match func(wire.FsChange) bool) {
	t.Helper()
	waitFor(t, func() bool { return h.has(match) }, func() string {
		return fmt.Sprintf("no matching event; saw %v", h.Events())
	})
}

func waitFor(t *testing.T, cond func() bool, why func() string) {
	t.Helper()
	end := time.Now().Add(deadline)
	for !cond() {
		if time.Now().After(end) {
			t.Fatal(why())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func isFs(p string) func(wire.FsChange) bool {
	return func(c wire.FsChange) bool { return c.Path == p }
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// a write under a watched root emits fs.change create with no agentId
func TestWriteEmitsCreateWithoutAgent(t *testing.T) {
	dir := tempDir(t)
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC).UnixMilli()
	h := newHarness(t, Options{Now: func() int64 { return now }})
	h.src.SetRoots([]string{dir})
	file := filepath.Join(dir, "src", "a.ts")
	mkdir(t, filepath.Join(dir, "src"))
	writeFile(t, file, "x")
	h.waitEvent(t, func(c wire.FsChange) bool { return c.Path == file && c.Op == "create" })
	var first wire.SourceEvent
	for _, e := range h.Events() {
		if c, ok := e.Ev.(wire.FsChange); ok && c.Path == file {
			first = e
			break
		}
	}
	want := wire.SourceEvent{Ts: "2026-11-17T10:00:00.000Z", Ev: wire.FsChange{Path: file, Op: "create"}}
	if first != want {
		t.Errorf("first event = %+v, want %+v", first, want)
	}
	b, err := json.Marshal(first.Ev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "agentId") {
		t.Errorf("event %s has an agentId", b)
	}
	// The directory itself is never emitted.
	if h.has(isFs(filepath.Join(dir, "src"))) {
		t.Errorf("the directory was emitted: %v", h.Events())
	}
	// A later write is reported again (macOS reports it as a rename, Linux as a change); a removal is a delete.
	time.Sleep(300 * time.Millisecond)
	before := len(h.Events())
	appendFile(t, file, "y")
	waitFor(t, func() bool { return len(h.Events()) > before }, func() string { return "no event after the append" })
	for _, e := range h.Events()[before:] {
		if c, ok := e.Ev.(wire.FsChange); !ok || (c.Op != "create" && c.Op != "change") {
			t.Errorf("event after the append = %+v, want create or change", e)
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, func(c wire.FsChange) bool { return c.Op == "delete" })
}

// node_modules, .git and .gitignored paths emit nothing
func TestIgnoredPathsEmitNothing(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "node_modules"))
	mkdir(t, filepath.Join(dir, ".git"))
	mkdir(t, filepath.Join(dir, "gen"))
	writeFile(t, filepath.Join(dir, ".gitignore"), "gen/\n*.log\n")
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{dir})
	writeFile(t, filepath.Join(dir, "node_modules", "x.js"), "1")
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "1")
	writeFile(t, filepath.Join(dir, "gen", "out.ts"), "1")
	writeFile(t, filepath.Join(dir, "debug.log"), "1")
	writeFile(t, filepath.Join(dir, "keep.ts"), "1")
	h.waitEvent(t, isFs(filepath.Join(dir, "keep.ts")))
	time.Sleep(150 * time.Millisecond)
	for _, e := range h.Events() {
		c := e.Ev.(wire.FsChange)
		p, err := filepath.Rel(dir, c.Path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(p, "node_modules") || strings.HasPrefix(p, ".git"+string(filepath.Separator)) ||
			strings.HasPrefix(p, "gen") || p == "debug.log" {
			t.Errorf("ignored path emitted: %s", p)
		}
	}
}

// RateLimiter allows 50 per second per key and counts the rest as dropped
func TestRateLimiter(t *testing.T) {
	var now int64
	limiter := NewRateLimiter(50, func() int64 { return now })
	allowed := 0
	for range 60 {
		if limiter.Allow("a") {
			allowed++
		}
	}
	if allowed != 50 {
		t.Errorf("allowed %d, want 50", allowed)
	}
	// Another key has its own window.
	if !limiter.Allow("b") {
		t.Error("key b was refused")
	}
	now = 999
	if limiter.Allow("a") {
		t.Error("key a was allowed at 999 ms")
	}
	now = 1001
	if !limiter.Allow("a") {
		t.Error("key a was refused at 1001 ms")
	}
}

// FsChangeSource counts events past the limit as dropped
func TestSourceCountsDropped(t *testing.T) {
	dir := tempDir(t)
	h := newHarness(t, Options{MaxPerSecond: 3, Now: func() int64 { return 0 }})
	h.src.SetRoots([]string{dir})
	for i := range 30 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), "1")
	}
	waitFor(t, func() bool { return h.src.Dropped() >= 1 }, func() string { return "nothing dropped" })
	time.Sleep(150 * time.Millisecond)
	if got := len(h.Events()); got != 3 {
		t.Errorf("got %d events, want 3", got)
	}
	if got := h.src.Dropped(); got < 1 {
		t.Errorf("Dropped() = %d, want at least 1", got)
	}
}

// setRoots refuses home, its ancestors and /, and replaces watchers on diff
func TestSetRootsRefusesBroadRootsAndDiffs(t *testing.T) {
	dir := tempDir(t)
	home := filepath.Join(dir, "home")
	a := filepath.Join(dir, "home", "a")
	b := filepath.Join(dir, "home", "b")
	mkdir(t, a)
	mkdir(t, b)
	h := newHarness(t, Options{Home: home})
	refused := func() []statusRec {
		var out []statusRec
		for _, x := range h.Statuses() {
			if x.s.Detail == "refused: too broad" {
				out = append(out, x)
			}
		}
		return out
	}

	h.src.SetRoots([]string{home, "/", dir, a})
	if got := h.src.Roots(); !slices.Equal(got, []string{a}) {
		t.Errorf("Roots() = %v, want [%s]", got, a)
	}
	var sources []string
	for _, x := range refused() {
		sources = append(sources, x.source)
		if x.s.State != "degraded" {
			t.Errorf("refused %s has state %q, want degraded", x.source, x.s.State)
		}
	}
	slices.Sort(sources)
	want := []string{"fs:" + home, "fs:/", "fs:" + dir}
	slices.Sort(want)
	if !slices.Equal(sources, want) {
		t.Errorf("refused sources = %v, want %v", sources, want)
	}
	// Each refused root is reported once, however often setRoots repeats.
	h.src.SetRoots([]string{home, "/", dir, a})
	if got := len(refused()); got != 3 {
		t.Errorf("got %d refused reports, want 3", got)
	}

	// Diff: a is closed, b is started.
	h.src.SetRoots([]string{b})
	if got := h.src.Roots(); !slices.Equal(got, []string{b}) {
		t.Errorf("Roots() = %v, want [%s]", got, b)
	}
	writeFile(t, filepath.Join(a, "old.txt"), "1")
	writeFile(t, filepath.Join(b, "new.txt"), "1")
	h.waitEvent(t, isFs(filepath.Join(b, "new.txt")))
	time.Sleep(100 * time.Millisecond)
	if h.has(isFs(filepath.Join(a, "old.txt"))) {
		t.Error("the closed root a still reported a change")
	}

	// A root nested in another watched root is skipped.
	h.src.SetRoots([]string{b, filepath.Join(b, "sub")})
	if got := h.src.Roots(); !slices.Equal(got, []string{b}) {
		t.Errorf("Roots() = %v, want [%s]", got, b)
	}
	h.src.Stop()
	if got := h.src.Roots(); len(got) != 0 {
		t.Errorf("Roots() after Stop = %v, want []", got)
	}
}

// a missing root is reported degraded and starts once it exists
func TestMissingRootStartsOnceItExists(t *testing.T) {
	dir := tempDir(t)
	root := filepath.Join(dir, "later")
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{root})
	if got := h.src.Roots(); len(got) != 0 {
		t.Errorf("Roots() = %v, want []", got)
	}
	if len(h.statusesOf("fs:"+root, "degraded")) == 0 {
		t.Errorf("no degraded status for the missing root: %v", h.Statuses())
	}
	mkdir(t, root)
	h.src.SetRoots([]string{root})
	if got := h.src.Roots(); !slices.Equal(got, []string{root}) {
		t.Errorf("Roots() = %v, want [%s]", got, root)
	}
}

// IGNORED_DIRS holds the prototype list plus .superpowers, target and out
func TestIgnoredDirs(t *testing.T) {
	for _, d := range []string{".git", "node_modules", ".worktrees", "dist", "build", ".serena", ".superpowers", "target", "out"} {
		if !IgnoredDirs[d] {
			t.Errorf("IgnoredDirs lacks %s", d)
		}
	}
	if len(IgnoredDirs) != 17 {
		t.Errorf("IgnoredDirs has %d names, want 17", len(IgnoredDirs))
	}
}

// GitignoreMatcher reads root, nested and info/exclude rules and refreshes on invalidate
func TestGitignoreMatcherRules(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, ".git", "info"))
	mkdir(t, filepath.Join(dir, "pkg", "inner"))
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.tmp\nroot-only.txt\n")
	writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), "excluded.txt\n")
	writeFile(t, filepath.Join(dir, "pkg", ".gitignore"), "local.txt\n/anchored.txt\n!keep.tmp\n")
	m := NewGitignoreMatcher(dir)
	check := func(rel string, want bool) {
		t.Helper()
		if got := m.IsIgnored(filepath.Join(dir, filepath.FromSlash(rel))); got != want {
			t.Errorf("IsIgnored(%s) = %v, want %v", rel, got, want)
		}
	}
	check("a.tmp", true)
	check("pkg/inner/b.tmp", true)
	check("excluded.txt", true)
	check("a.ts", false)
	// Nested rules apply relative to their own directory.
	check("pkg/inner/local.txt", true)
	check("local.txt", false)
	check("pkg/anchored.txt", true)
	check("pkg/inner/anchored.txt", false)
	check("pkg/keep.tmp", false)
	// Paths outside the root are not ignored.
	if m.IsIgnored(filepath.Join(filepath.Dir(dir), "x.tmp")) {
		t.Error("a path outside the root is ignored")
	}

	// Cached until the .gitignore changes.
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.tmp\nroot-only.txt\nnew.txt\n")
	check("new.txt", false)
	m.Invalidate(filepath.Join(dir, ".gitignore"))
	check("new.txt", true)
}

// GitignoreMatcher ignores .git/info/exclude when .git is a file
func TestGitignoreMatcherGitFile(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, ".git"), "gitdir: /elsewhere\n")
	m := NewGitignoreMatcher(dir)
	if m.IsIgnored(filepath.Join(dir, "a.ts")) {
		t.Error("a.ts is ignored")
	}
}

// a .gitignore edit takes effect for later events
func TestGitignoreEditTakesEffect(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "a.txt\n")
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{dir})
	writeFile(t, filepath.Join(dir, ".gitignore"), "a.txt\nb.txt\n")
	h.waitEvent(t, isFs(filepath.Join(dir, ".gitignore")))
	writeFile(t, filepath.Join(dir, "b.txt"), "1")
	writeFile(t, filepath.Join(dir, "c.txt"), "1")
	h.waitEvent(t, isFs(filepath.Join(dir, "c.txt")))
	time.Sleep(100 * time.Millisecond)
	if h.has(isFs(filepath.Join(dir, "b.txt"))) {
		t.Error("b.txt was reported after the .gitignore edit ignored it")
	}
}

// default home is os.homedir()
func TestDefaultHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory:", err)
	}
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{home})
	if got := h.src.Roots(); len(got) != 0 {
		t.Errorf("Roots() = %v, want []", got)
	}
	if !slices.ContainsFunc(h.Statuses(), func(x statusRec) bool { return x.s.Detail == "refused: too broad" }) {
		t.Errorf("home was not refused: %v", h.Statuses())
	}
}

// a rename event for a file older than one second is reported as change
func TestOldFileRenameIsChange(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "old.txt")
	writeFile(t, file, "x")
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{dir})
	time.Sleep(1150 * time.Millisecond)
	before := len(h.Events())
	appendFile(t, file, "y")
	waitFor(t, func() bool { return len(h.Events()) > before }, func() string { return "no event after the append" })
	e := h.Events()[before]
	wantOp := "create"
	if fsutil.BirthMs(file) > 0 {
		wantOp = "change"
	}
	if want := (wire.FsChange{Path: file, Op: wantOp}); e.Ev != want {
		t.Errorf("event = %+v, want %+v", e.Ev, want)
	}
}

// a brand-new file is still reported as create
//
// On macOS every write is a rename, so every event must be create, as the
// TypeScript test expects. Elsewhere (inotify) a write is a change event and
// is reported as change, as in the TypeScript code; the first event, the
// file's creation, must still be create.
func TestNewFileIsCreate(t *testing.T) {
	dir := tempDir(t)
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{dir})
	file := filepath.Join(dir, "new.txt")
	writeFile(t, file, "x")
	appendFile(t, file, "y")
	h.waitEvent(t, isFs(file))
	for i, e := range h.Events() {
		c, ok := e.Ev.(wire.FsChange)
		if ok && (c.Op == "create" || (i > 0 && runtime.GOOS != "darwin" && c.Op == "change")) {
			continue
		}
		t.Errorf("event %d = %+v, want create", i, e)
	}
}

// a deleted root is reported degraded and dropped, and restarts when it is recreated
//
// The TypeScript test expects exactly one degraded status. That holds for
// Bun's fs.watch on macOS, which does not fail when the root goes. The Go
// watchers (both backends) degrade with "watcher error on <root>: ENOENT"
// when the root is removed, so the source reports that, and then
// "cannot watch: root not found" from resolve, as the TypeScript code does
// on a watcher that errors. FSEvents may also deliver the root event late.
// So this test checks that "root not found" is reported once and that a
// repeated call reports nothing more.
func TestDeletedRootRestarts(t *testing.T) {
	dir := tempDir(t)
	root := filepath.Join(dir, "proj")
	mkdir(t, root)
	h := newHarness(t, Options{})
	h.src.SetRoots([]string{root})
	if got := h.src.Roots(); !slices.Equal(got, []string{root}) {
		t.Fatalf("Roots() = %v, want [%s]", got, root)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	h.src.SetRoots([]string{root})
	if got := h.src.Roots(); len(got) != 0 {
		t.Errorf("Roots() = %v, want []", got)
	}
	degraded := func() []statusRec { return h.statusesOf("fs:"+root, "degraded") }
	notFound := func() int {
		n := 0
		for _, x := range degraded() {
			if x.s.Detail == "cannot watch: root not found" {
				n++
			}
		}
		return n
	}
	if got := notFound(); got != 1 {
		t.Errorf("got %d root-not-found reports, want 1: %v", got, degraded())
	}
	count := len(degraded())
	// Repeated calls do not repeat the report.
	h.src.SetRoots([]string{root})
	if got := len(degraded()); got != count {
		t.Errorf("got %d degraded reports after a repeated call, want %d: %v", got, count, degraded())
	}

	mkdir(t, root)
	okBefore := len(h.statusesOf("fs:"+root, "ok"))
	h.src.SetRoots([]string{root})
	if got := h.src.Roots(); !slices.Equal(got, []string{root}) {
		t.Errorf("Roots() = %v, want [%s]", got, root)
	}
	if got := len(h.statusesOf("fs:"+root, "ok")); got != okBefore+1 {
		t.Errorf("got %d ok reports, want %d", got, okBefore+1)
	}
	writeFile(t, filepath.Join(root, "again.txt"), "1")
	h.waitEvent(t, isFs(filepath.Join(root, "again.txt")))
}

// fakeWatcher is a watcher whose failure the test sets.
type fakeWatcher struct {
	mu       sync.Mutex
	degraded string
	partial  bool
	closed   bool
}

func (f *fakeWatcher) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeWatcher) Degraded() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.degraded
}

func (f *fakeWatcher) Partial() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.partial && !f.closed
}

// fail is a failure that needs a restart. The first reason stays.
func (f *fakeWatcher) fail(why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.degraded == "" {
		f.degraded = why
	}
	f.partial = false
}

// limit is a capacity limit: the watcher runs on and covers part of the tree.
func (f *fakeWatcher) limit(why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.degraded == "" {
		f.degraded = why
		f.partial = true
	}
}

func (f *fakeWatcher) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// a watcher that errors is reported once, closed and dropped, then restarted on a later setRoots
func TestFailedWatcherRestarts(t *testing.T) {
	dir := tempDir(t)
	var fakes []*fakeWatcher
	h := newHarness(t, Options{Watch: func(string, func(string, watch.Op), watch.Options) watch.Watcher {
		f := &fakeWatcher{}
		fakes = append(fakes, f)
		return f
	}})
	status := func(state string) []statusRec { return h.statusesOf("fs:"+dir, state) }

	h.src.SetRoots([]string{dir})
	if len(fakes) != 1 {
		t.Fatalf("started %d watchers, want 1", len(fakes))
	}
	if got := len(status("ok")); got != 1 {
		t.Errorf("got %d ok reports, want 1", got)
	}

	// The watcher dies (as when the root is deleted on Linux) and delivers no events.
	fakes[0].fail("watcher error on root: ENOENT")
	h.src.SetRoots([]string{dir})
	var details []string
	for _, x := range status("degraded") {
		details = append(details, x.s.Detail)
	}
	if want := []string{"watcher error on root: ENOENT"}; !slices.Equal(details, want) {
		t.Errorf("degraded details = %q, want %q", details, want)
	}
	if !fakes[0].isClosed() {
		t.Error("the failed watcher was not closed")
	}
	if got := h.src.Roots(); len(got) != 0 {
		t.Errorf("Roots() = %v, want []", got)
	}
	if len(fakes) != 1 {
		t.Errorf("started %d watchers, want 1", len(fakes))
	}

	// The next call restarts it and reports ok; nothing more is reported degraded.
	h.src.SetRoots([]string{dir})
	if len(fakes) != 2 {
		t.Errorf("started %d watchers, want 2", len(fakes))
	}
	if got := h.src.Roots(); !slices.Equal(got, []string{dir}) {
		t.Errorf("Roots() = %v, want [%s]", got, dir)
	}
	if got := len(status("ok")); got != 2 {
		t.Errorf("got %d ok reports, want 2", got)
	}
	if got := len(status("degraded")); got != 1 {
		t.Errorf("got %d degraded reports, want 1", got)
	}
}

// A partial watcher (a capacity limit) still reports changes in what it
// covers. SetRoots reports it once and keeps it: it does not close it nor
// start another. A later failure that needs a restart closes it on the next
// call and restarts it on the call after, as for any failed watcher.
func TestPartialWatcherKept(t *testing.T) {
	dir := tempDir(t)
	var fakes []*fakeWatcher
	h := newHarness(t, Options{Watch: func(string, func(string, watch.Op), watch.Options) watch.Watcher {
		f := &fakeWatcher{}
		fakes = append(fakes, f)
		return f
	}})
	status := func(state string) []statusRec { return h.statusesOf("fs:"+dir, state) }
	capped := "more than 12000 directories; later directories are not watched"

	h.src.SetRoots([]string{dir})
	fakes[0].limit(capped)
	for range 3 {
		h.src.SetRoots([]string{dir})
	}
	if len(fakes) != 1 {
		t.Errorf("started %d watchers, want 1", len(fakes))
	}
	if fakes[0].isClosed() {
		t.Error("the partial watcher was closed")
	}
	if got := h.src.Roots(); !slices.Equal(got, []string{dir}) {
		t.Errorf("Roots() = %v, want [%s]", got, dir)
	}
	want := []statusRec{{"fs:" + dir, wire.SourceState{State: "degraded", Detail: capped}}}
	if got := status("degraded"); !slices.Equal(got, want) {
		t.Errorf("degraded reports = %v, want %v", got, want)
	}

	// The watcher then fails for good: Degraded keeps the first reason.
	fakes[0].fail("events dropped under load")
	h.src.SetRoots([]string{dir})
	if !fakes[0].isClosed() {
		t.Error("the failed watcher was not closed")
	}
	if got := h.src.Roots(); len(got) != 0 {
		t.Errorf("Roots() = %v, want []", got)
	}
	h.src.SetRoots([]string{dir})
	if len(fakes) != 2 {
		t.Errorf("started %d watchers, want 2", len(fakes))
	}
	if got := len(status("ok")); got != 2 {
		t.Errorf("got %d ok reports, want 2", got)
	}
	if got := status("degraded"); !slices.Equal(got, want) {
		t.Errorf("degraded reports = %v, want %v", got, want)
	}
}
