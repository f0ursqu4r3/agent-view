// Package fschange watches agents' project roots and emits fs.change
// events. It replaces src/collector/sources/fs-changes.ts. The events never
// carry an agent id: the viewer draws them as unattributed activity.
package fschange

import (
	"cmp"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"agent-view/internal/fsutil"
	"agent-view/internal/watch"
	"agent-view/internal/wire"
)

const (
	// maxDirs bounds the watched directories per root (fsnotify backend).
	maxDirs             = 12_000
	defaultMaxPerSecond = 50
)

// IgnoredDirs are the directory names that are never reported, wherever
// they appear in a path.
var IgnoredDirs = map[string]bool{
	".git": true, ".worktrees": true, ".pytest_cache": true, "__pycache__": true, ".turbo": true,
	"coverage": true, "node_modules": true, ".next": true, ".cache": true, ".venv": true,
	"venv": true, "dist": true, "build": true, ".serena": true, ".superpowers": true,
	"target": true, "out": true,
}

// Options configure a Source. Emit and OnStatus are required; both may be
// called from any goroutine.
type Options struct {
	Emit     func([]wire.SourceEvent)
	OnStatus func(source string, s wire.SourceState)
	// MaxPerSecond is the most events per root per second; 0 means 50.
	MaxPerSecond int
	// Home is the directory that is never watched, nor any of its
	// ancestors; "" means the user's home directory.
	Home string
	// Now returns the time in Unix milliseconds; nil means wire.NowMs.
	Now func() int64
	// Watch starts a tree watcher; nil means watch.Tree. Tests replace it.
	Watch func(root string, onChange func(string, watch.Op), o watch.Options) watch.Watcher
}

// Source watches project roots and emits fs.change events.
//
// SetRoots and Stop may be called from any goroutine. They wait for a
// running watcher callback (and its Emit or OnStatus) to finish, so Emit
// and OnStatus must not block on the goroutine that calls them, and must
// not call SetRoots or Stop.
type Source struct {
	emit     func([]wire.SourceEvent)
	onStatus func(source string, s wire.SourceState)
	now      func() int64
	watch    func(root string, onChange func(string, watch.Op), o watch.Options) watch.Watcher
	home     string

	// ctl serializes SetRoots and Stop. The watcher callbacks never take
	// it, so a watcher may be closed while it is held.
	ctl sync.Mutex
	// reported are the roots already reported as refused or missing, so a
	// repeated SetRoots stays quiet. Guarded by ctl.
	reported map[string]bool

	// mu guards the fields below. It is never held across a watcher call,
	// Emit or OnStatus.
	mu      sync.Mutex
	limiter *RateLimiter
	active  map[string]watch.Watcher
	order   []string // the keys of active, in start order
	// degradedSeen is the degraded detail last reported for each root, so
	// a repeated failure is reported once.
	degradedSeen map[string]string
	dropped      int
}

// New returns a Source that watches nothing yet.
func New(o Options) *Source {
	now := o.Now
	if now == nil {
		now = wire.NowMs
	}
	perSecond := o.MaxPerSecond
	if perSecond <= 0 {
		perSecond = defaultMaxPerSecond
	}
	home := o.Home
	if home == "" {
		home = homeDir()
	}
	watchFn := o.Watch
	if watchFn == nil {
		watchFn = watch.Tree
	}
	return &Source{
		emit:         o.Emit,
		onStatus:     o.OnStatus,
		now:          now,
		watch:        watchFn,
		home:         realOr(home),
		reported:     map[string]bool{},
		limiter:      NewRateLimiter(perSecond, now),
		active:       map[string]watch.Watcher{},
		degradedSeen: map[string]string{},
	}
}

// Dropped is the number of events dropped by the rate limit.
func (s *Source) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Roots are the real paths watched now, in start order.
func (s *Source) Roots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.order...)
}

// SetRoots watches roots: it refuses broad roots, skips missing ones and
// those nested in another, stops the watchers no longer wanted and starts
// the new ones.
func (s *Source) SetRoots(roots []string) {
	s.ctl.Lock()
	defer s.ctl.Unlock()
	// A watcher that has failed delivers no events. Report it, close it and
	// drop it here; a later call restarts it. A partial watcher (a capacity
	// or permission limit) still delivers the events of what it covers, and
	// a restart would walk the whole tree again to hit the same limit:
	// report it and keep it.
	failed := map[string]bool{}
	for _, root := range s.Roots() {
		w := s.watcher(root)
		if w.Degraded() == "" {
			continue
		}
		s.reportIfDegraded(root)
		if w.Partial() {
			continue
		}
		s.drop(root).Close()
		failed[root] = true
	}
	wanted := s.resolve(roots)
	for _, root := range s.Roots() {
		if !slices.Contains(wanted, root) {
			s.drop(root).Close()
		}
	}
	for _, root := range wanted {
		if s.watcher(root) == nil && !failed[root] {
			s.start(root)
		}
	}
}

// Stop closes every watcher.
func (s *Source) Stop() {
	s.ctl.Lock()
	defer s.ctl.Unlock()
	for _, root := range s.Roots() {
		s.drop(root).Close()
	}
}

func (s *Source) watcher(root string) watch.Watcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[root]
}

// drop forgets the watcher of root and returns it, for the caller to close
// without holding mu.
func (s *Source) drop(root string) watch.Watcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.active[root]
	delete(s.active, root)
	s.order = slices.DeleteFunc(s.order, func(r string) bool { return r == root })
	return w
}

// resolve returns the real paths of the roots to watch: it refuses broad
// roots, skips missing ones and drops nested ones. It runs under ctl.
func (s *Source) resolve(roots []string) []string {
	seen := map[string]bool{}
	for _, root := range roots {
		seen[root] = true
	}
	for key := range s.reported {
		if !seen[key] {
			delete(s.reported, key)
		}
	}
	report := func(root, detail string) {
		if s.reported[root] {
			return
		}
		s.reported[root] = true
		s.onStatus("fs:"+root, wire.SourceState{State: "degraded", Detail: detail})
	}
	real := map[string]bool{}
	for _, root := range roots {
		rp, err := realpath(root)
		if err != nil {
			report(root, "cannot watch: root not found")
			continue
		}
		if !isDirectory(rp) {
			report(root, "cannot watch: not a directory")
			continue
		}
		if rp == filepath.VolumeName(rp)+string(filepath.Separator) || isWithin(s.home, rp) {
			report(root, "refused: too broad")
			continue
		}
		delete(s.reported, root)
		real[rp] = true
	}
	// Shortest first, so a parent is kept and the roots nested in it are
	// skipped. Equal lengths sort by plain string order (TypeScript uses
	// localeCompare; the order of equal-length roots does not matter).
	sorted := make([]string, 0, len(real))
	for rp := range real {
		sorted = append(sorted, rp)
	}
	slices.SortFunc(sorted, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
	})
	var kept []string
	for _, rp := range sorted {
		if !slices.ContainsFunc(kept, func(k string) bool { return isWithin(rp, k) }) {
			kept = append(kept, rp)
		}
	}
	return kept
}

// start watches root and reports it ok, or degraded when the watcher
// failed at once. It runs under ctl.
func (s *Source) start(root string) {
	matcher := NewGitignoreMatcher(root)
	exclude := filepath.Join(".git", "info", "exclude")
	ignored := func(rel string) bool {
		abs := filepath.Join(root, rel)
		if filepath.Base(rel) == ".gitignore" {
			matcher.Invalidate(abs)
		} else if rel == exclude {
			matcher.Invalidate(filepath.Join(root, ".gitignore"))
		}
		for _, seg := range strings.Split(rel, string(filepath.Separator)) {
			if IgnoredDirs[seg] {
				return true
			}
		}
		return matcher.IsIgnored(abs)
	}
	w := s.watch(root, func(rel string, op watch.Op) { s.onChange(root, rel, op) },
		watch.Options{Ignore: ignored, MaxDirs: maxDirs})
	s.mu.Lock()
	s.active[root] = w
	s.order = append(s.order, root)
	status, degraded := s.degradedLocked(root)
	if !degraded {
		delete(s.degradedSeen, root)
		status = &wire.SourceState{State: "ok"}
	}
	s.mu.Unlock()
	if status != nil {
		s.onStatus("fs:"+root, *status)
	}
}

// onChange runs on a watcher goroutine.
func (s *Source) onChange(root, rel string, op watch.Op) {
	abs := filepath.Join(root, rel)
	fi, err := os.Lstat(abs)
	if err == nil && fi.IsDir() {
		return
	}
	kind := "create"
	switch {
	case op == watch.Change:
		kind = "change"
	case err != nil:
		kind = "delete"
	case looksModified(abs, fi):
		kind = "change"
	}
	s.mu.Lock()
	allowed := s.limiter.Allow(root)
	if !allowed {
		s.dropped++
	}
	s.mu.Unlock()
	if !allowed {
		return
	}
	s.emit([]wire.SourceEvent{{Ts: wire.FormatISO(s.now()), Ev: wire.FsChange{Path: abs, Op: kind}}})
	s.reportIfDegraded(root)
}

// reportIfDegraded reports a watcher's degraded reason once. It returns
// true when the watcher is degraded.
func (s *Source) reportIfDegraded(root string) bool {
	s.mu.Lock()
	status, degraded := s.degradedLocked(root)
	s.mu.Unlock()
	if status != nil {
		s.onStatus("fs:"+root, *status)
	}
	return degraded
}

// degradedLocked tells whether the watcher of root is degraded, and returns
// the status to report when its reason was not reported yet.
func (s *Source) degradedLocked(root string) (*wire.SourceState, bool) {
	w := s.active[root]
	if w == nil {
		return nil, false
	}
	detail := w.Degraded()
	if detail == "" {
		return nil, false
	}
	if s.degradedSeen[root] == detail {
		return nil, true
	}
	s.degradedSeen[root] = detail
	return &wire.SourceState{State: "degraded", Detail: detail}, true
}

// looksModified: macOS reports every write as a rename. A regular file
// whose last write is more than a second after its birth was modified, not
// created. Without a birth time (0) the answer stays create.
func looksModified(abs string, fi os.FileInfo) bool {
	if !fi.Mode().IsRegular() {
		return false
	}
	birth := fsutil.BirthMs(abs)
	mtime := float64(fi.ModTime().Unix())*1000 + float64(fi.ModTime().Nanosecond())/1e6
	return birth > 0 && mtime-birth > 1000
}

// homeDir is os.homedir(): $HOME, else the user's entry in the password
// database.
func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// realpath is fs.realpathSync: the absolute path with symlinks resolved.
func realpath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// realOr is the real path of p, or p made absolute when it has none.
func realOr(p string) string {
	if rp, err := realpath(p); err == nil {
		return rp
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// isWithin reports whether child is parent or below it.
func isWithin(child, parent string) bool {
	if child == parent {
		return true
	}
	if !strings.HasSuffix(parent, string(filepath.Separator)) {
		parent += string(filepath.Separator)
	}
	return strings.HasPrefix(child, parent)
}
