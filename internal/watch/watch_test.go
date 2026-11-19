package watch

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// waitFor is how long a test waits for an event or a state. FSEvents
// delivers in about 15-60 ms, but takes seconds while fseventsd is busy.
const waitFor = 10 * time.Second

type event struct {
	rel string
	op  Op
}

// recorder collects callbacks. A callback after Close has returned fails the test.
type recorder struct {
	t      *testing.T
	mu     sync.Mutex
	seen   []event
	closed atomic.Bool
}

func newRecorder(t *testing.T) *recorder { return &recorder{t: t} }

func (r *recorder) onChange(rel string, op Op) {
	if r.closed.Load() {
		r.t.Errorf("callback after Close: %s %s", op, rel)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, event{rel, op})
}

func (r *recorder) events() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

// wait polls until an event matches, for at most waitFor.
func (r *recorder) wait(match func(event) bool) event {
	r.t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		seen := r.events()
		if i := slices.IndexFunc(seen, match); i >= 0 {
			return seen[i]
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("no matching change; saw %v", seen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *recorder) waitRel(rel string) event {
	r.t.Helper()
	return r.wait(func(e event) bool { return e.rel == rel })
}

// close closes w and marks the recorder, so a later callback fails the test.
func (r *recorder) close(w Watcher) {
	w.Close()
	r.closed.Store(true)
}

type backend struct {
	name string
	tree func(root string, onChange func(rel string, op Op), o Options) Watcher
}

// backends are Tree (FSEvents on macOS) and the fsnotify backend.
var backends = []backend{{"Tree", Tree}, {"fsnotify", treeFsnotify}}

// writeOp is the op that b reports for a write to an existing file: FSEvents
// reports every event as Rename.
func writeOp(b backend) Op {
	if b.name == "Tree" && runtime.GOOS == "darwin" {
		return Rename
	}
	return Change
}

func mkdirAll(t *testing.T, p ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(p...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, file, data string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitDegraded polls until w reports want, for at most waitFor.
func waitDegraded(t *testing.T, w Watcher, want string) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for w.Degraded() != want {
		if time.Now().After(deadline) {
			t.Fatalf("Degraded() = %q, want %q", w.Degraded(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recursive mode reports a nested file as a relative path
func TestTreeNestedRelativePath(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, "a", "b")
	r := newRecorder(t)
	w := Tree(dir, r.onChange, Options{})
	defer r.close(w)
	if got := w.Degraded(); got != "" {
		t.Fatalf("Degraded() = %q", got)
	}
	writeFile(t, filepath.Join(dir, "a", "b", "x.txt"), "x")
	r.waitRel(filepath.Join("a", "b", "x.txt"))
}

// per-dir mode watches nested and newly created directories
func TestFsnotifyNewDirectories(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, "a", "b")
	r := newRecorder(t)
	w := treeFsnotify(dir, r.onChange, Options{})
	defer r.close(w)
	if got := w.Degraded(); got != "" {
		t.Fatalf("Degraded() = %q", got)
	}
	writeFile(t, filepath.Join(dir, "a", "b", "x.txt"), "x")
	r.waitRel(filepath.Join("a", "b", "x.txt"))
	mkdirAll(t, dir, "c", "d")
	writeFile(t, filepath.Join(dir, "c", "d", "y.txt"), "y")
	r.waitRel(filepath.Join("c", "d", "y.txt"))
}

// per-dir mode skips ignored and symlinked directories
func TestFsnotifySkipsIgnoredAndSymlinked(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	root := filepath.Join(dir, "root")
	mkdirAll(t, root, "node_modules")
	mkdirAll(t, root, "src")
	mkdirAll(t, outside)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	r := newRecorder(t)
	ignore := func(rel string) bool {
		return slices.Contains(strings.Split(rel, string(filepath.Separator)), "node_modules")
	}
	w := treeFsnotify(root, r.onChange, Options{Ignore: ignore})
	defer r.close(w)
	writeFile(t, filepath.Join(root, "node_modules", "ignored.txt"), "x")
	writeFile(t, filepath.Join(outside, "linked.txt"), "x")
	writeFile(t, filepath.Join(root, "src", "canary.txt"), "x")
	r.waitRel(filepath.Join("src", "canary.txt"))
	time.Sleep(150 * time.Millisecond)
	for _, e := range r.events() {
		if strings.Contains(e.rel, "ignored") || strings.Contains(e.rel, "linked") || strings.HasPrefix(e.rel, "link"+string(filepath.Separator)) {
			t.Errorf("reported %s %s", e.op, e.rel)
		}
	}
}

// per-dir mode stops at maxDirs and reports degraded
func TestFsnotifyMaxDirs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		mkdirAll(t, dir, name)
	}
	w := treeFsnotify(dir, func(string, Op) {}, Options{MaxDirs: 3})
	defer w.Close()
	if got, want := w.Degraded(), "more than 3 directories; later directories are not watched"; got != want {
		t.Errorf("Degraded() = %q, want %q", got, want)
	}
	if !w.Partial() {
		t.Error("Partial() = false at the directory cap, want true")
	}
	w.Close()
	if w.Partial() {
		t.Error("Partial() = true after Close, want false")
	}
}

// The directory cap leaves the watcher partial: it still reports changes in
// the directories it watches. A later removal of the root needs a restart:
// Partial turns false, and Degraded keeps the first reason.
func TestFsnotifyPartialUntilRootRemoved(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	for _, name := range []string{"a", "b", "c", "d"} {
		mkdirAll(t, root, name)
	}
	r := newRecorder(t)
	w := treeFsnotify(root, r.onChange, Options{MaxDirs: 3})
	defer r.close(w)
	capped := "more than 3 directories; later directories are not watched"
	if got := w.Degraded(); got != capped {
		t.Fatalf("Degraded() = %q, want %q", got, capped)
	}
	if !w.Partial() {
		t.Fatal("Partial() = false at the directory cap, want true")
	}
	writeFile(t, filepath.Join(root, "f.txt"), "x")
	r.waitRel("f.txt")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(waitFor)
	for w.Partial() {
		if time.Now().After(deadline) {
			t.Fatal("Partial() = true after the root was removed, want false")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := w.Degraded(); got != capped {
		t.Errorf("Degraded() = %q, want the first reason %q", got, capped)
	}
}

// A capacity failure on a directory (no inotify watches or file descriptors
// left) leaves the watcher partial. Any other failure needs a restart.
func TestFsnotifyFailCapacity(t *testing.T) {
	cases := []struct {
		err     error
		code    string
		partial bool
	}{
		{syscall.ENOSPC, "ENOSPC", true},
		{syscall.EMFILE, "EMFILE", true},
		{&fs.PathError{Op: "open", Path: "x", Err: syscall.ENFILE}, "ENFILE", true},
		{syscall.EACCES, "EACCES", false},
	}
	for _, c := range cases {
		s, failed := start(t.TempDir(), func(string, Op) {}, Options{})
		if failed != nil {
			t.Fatalf("start: %q", failed.Degraded())
		}
		x := &fsnotifyWatcher{state: s}
		sub := filepath.Join(s.real, "sub")
		x.fail("cannot watch", sub, c.err)
		if got, want := x.Degraded(), "cannot watch "+sub+": "+c.code; got != want {
			t.Errorf("%s: Degraded() = %q, want %q", c.code, got, want)
		}
		if got := x.Partial(); got != c.partial {
			t.Errorf("%s: Partial() = %v, want %v", c.code, got, c.partial)
		}
	}
}

// a missing root sets degraded and does not throw, in both modes
func TestTreeMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, b := range backends {
		w := b.tree(missing, func(string, Op) {}, Options{})
		if got, want := w.Degraded(), "cannot watch "+missing+": ENOENT"; got != want {
			t.Errorf("%s: Degraded() = %q, want %q", b.name, got, want)
		}
		if w.Partial() {
			t.Errorf("%s: Partial() = true for a watcher that could not start", b.name)
		}
		w.Close()
		w.Close()
	}
}

// close stops callbacks
func TestTreeCloseStopsCallbacks(t *testing.T) {
	dir := t.TempDir()
	for _, b := range backends {
		r := newRecorder(t)
		w := b.tree(dir, r.onChange, Options{})
		r.close(w)
		w.Close()
		writeFile(t, filepath.Join(dir, b.name+".txt"), "x")
		time.Sleep(200 * time.Millisecond)
		if seen := r.events(); len(seen) != 0 {
			t.Errorf("%s: saw %v", b.name, seen)
		}
	}
}

// A root given through a symlink reports paths relative to the real root.
// A write to an existing file reports Change on fsnotify and Rename on FSEvents.
func TestTreeSymlinkRoot(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			dir := t.TempDir()
			real := filepath.Join(dir, "real")
			mkdirAll(t, real, "sub")
			file := filepath.Join(real, "sub", "f.txt")
			writeFile(t, file, "a")
			link := filepath.Join(dir, "link")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			r := newRecorder(t)
			w := b.tree(link, r.onChange, Options{})
			defer r.close(w)
			if got := w.Degraded(); got != "" {
				t.Fatalf("Degraded() = %q", got)
			}
			writeFile(t, file, "b")
			e := r.waitRel(filepath.Join("sub", "f.txt"))
			if want := writeOp(b); e.op != want {
				t.Errorf("op = %s, want %s", e.op, want)
			}
		})
	}
}

// Removing the root sets Degraded() to "watcher error on <root>: ENOENT".
func TestTreeRootRemoved(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			mkdirAll(t, root, "sub")
			writeFile(t, filepath.Join(root, "sub", "f.txt"), "x")
			w := b.tree(root, func(string, Op) {}, Options{})
			defer w.Close()
			if got := w.Degraded(); got != "" {
				t.Fatalf("Degraded() = %q", got)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			waitDegraded(t, w, "watcher error on "+root+": ENOENT")
		})
	}
}

// An fsnotify error maps to a degraded reason: overflow is a drop, EINVAL
// from removing a watch the kernel already dropped is ignored, and any other
// error is a watcher error on the root.
func TestFsnotifyErrorReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fsnotify.ErrEventOverflow, "events dropped under load"},
		{fmt.Errorf("remove: %w", syscall.EINVAL), ""},
		{&fs.PathError{Op: "read", Path: "/r/x", Err: syscall.EACCES}, "watcher error on /r: EACCES"},
	}
	for _, c := range cases {
		if got := errorReason("/r", c.err); got != c.want {
			t.Errorf("errorReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
