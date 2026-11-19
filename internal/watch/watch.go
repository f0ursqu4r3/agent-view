// Package watch reports file changes under a directory tree. It replaces the
// former TypeScript collector/watch.ts (removed in P2c; see git history):
// FSEvents on macOS, fsnotify (one watch per directory) elsewhere.
package watch

import (
	"path/filepath"
	"strings"
	"sync/atomic"

	"agent-view/internal/fsutil"
)

// Op is the kind of a change, with the event names of Node's fs.watch.
type Op int

const (
	// Rename is a create, a removal or a rename. FSEvents reports every
	// change as Rename.
	Rename Op = iota
	// Change is a write or a metadata change.
	Change
)

func (o Op) String() string {
	if o == Change {
		return "change"
	}
	return "rename"
}

// defaultMaxDirs is the directory limit of the fsnotify backend.
const defaultMaxDirs = 12_000

// Options tune a watcher.
type Options struct {
	// Ignore filters every event by its path relative to the root. The
	// fsnotify backend also does not walk into an ignored directory. Like
	// onChange, it may run on a watcher goroutine.
	Ignore func(rel string) bool
	// MaxDirs is the most directories the fsnotify backend watches; 0 means
	// 12,000.
	MaxDirs int
}

// Watcher is a running tree watcher.
type Watcher interface {
	// Close stops the watcher. After Close returns, no callback runs. Close
	// is idempotent. It waits for a running callback, so it must not be
	// called from onChange or while holding a lock that onChange takes.
	Close()
	// Degraded is "" while the watcher is healthy, else the first reason it
	// failed. It is safe from any goroutine.
	Degraded() string
	// Partial is true while the watcher runs but covers only part of the
	// tree because of a limit that a restart would hit again: the directory
	// limit, no inotify watches or file descriptors left, or a directory
	// below the root that it may not read. Such a watcher still reports
	// changes in what it covers. Partial is false while the watcher is
	// healthy, after Close, and after any failure that needs a restart (the
	// root removed, a watcher error, events dropped), even though Degraded
	// keeps its first reason. It is safe from any goroutine.
	Partial() bool
}

// Tree watches the directory tree at root and calls onChange with each
// changed path relative to root, resolved with filepath.EvalSymlinks. An event
// for the root itself is not reported. onChange may run on a watcher
// goroutine. Failures never panic: they set Degraded, and the watcher keeps
// what it has. Tree uses FSEvents on macOS and fsnotify elsewhere.
func Tree(root string, onChange func(rel string, op Op), o Options) Watcher {
	return native(root, onChange, o)
}

// state is what both backends share.
type state struct {
	root     string // as the caller gave it, for messages
	real     string // root resolved with filepath.EvalSymlinks
	prefix   string // real followed by a separator
	onChange func(rel string, op Op)
	ignore   func(rel string) bool
	degraded atomic.Pointer[string]
	// fatal is set by a failure that needs a restart.
	fatal  atomic.Bool
	closed atomic.Bool
}

// start resolves root. When root cannot be resolved, it returns a watcher
// that reports why, and nothing runs.
func start(root string, onChange func(rel string, op Op), o Options) (*state, Watcher) {
	real, err := filepath.Abs(root)
	if err == nil {
		real, err = filepath.EvalSymlinks(real)
	}
	if err != nil {
		return nil, stopped("cannot watch " + root + ": " + fsutil.ErrCode(err))
	}
	return &state{
		root:     root,
		real:     real,
		prefix:   strings.TrimSuffix(real, string(filepath.Separator)) + string(filepath.Separator),
		onChange: onChange,
		ignore:   o.Ignore,
	}, nil
}

// degrade records a failure that needs a restart. The first reason stays.
func (s *state) degrade(why string) {
	// fatal goes first, so Partial never sees this reason without it.
	s.fatal.Store(true)
	s.degraded.CompareAndSwap(nil, &why)
}

// limit records a limit that a restart would hit again: the watcher runs
// on, but covers only part of the tree. The first reason stays.
func (s *state) limit(why string) { s.degraded.CompareAndSwap(nil, &why) }

func (s *state) Partial() bool {
	return s.degraded.Load() != nil && !s.fatal.Load() && !s.closed.Load()
}

func (s *state) Degraded() string {
	if p := s.degraded.Load(); p != nil {
		return *p
	}
	return ""
}

// rel returns abs relative to the real root: "" for the root itself, and
// false for a path outside it.
func (s *state) rel(abs string) (string, bool) {
	if abs == s.real {
		return "", true
	}
	if rel, ok := strings.CutPrefix(abs, s.prefix); ok && rel != "" {
		return rel, true
	}
	return "", false
}

func (s *state) ignored(rel string) bool { return s.ignore != nil && s.ignore(rel) }

// emit reports a change unless the watcher is closed or rel is ignored.
func (s *state) emit(rel string, op Op) {
	if s.closed.Load() || s.ignored(rel) {
		return
	}
	// A consumer error must not break the watcher.
	defer func() { _ = recover() }()
	s.onChange(rel, op)
}

// stopped is a watcher that could not start.
type stopped string

func (stopped) Close()             {}
func (s stopped) Degraded() string { return string(s) }
func (stopped) Partial() bool      { return false }
