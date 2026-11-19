package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"

	"agent-view/internal/fsutil"
)

// fsnotifyWatcher is the per-directory mode of watch.ts: one fsnotify watch
// per directory, found by a walk. A new directory is added on Create, then
// read, and its entries are reported, so files written before the add are
// not missed. Symlinked directories are not followed.
type fsnotifyWatcher struct {
	*state
	w       *fsnotify.Watcher
	maxDirs int
	// dirs are the directories watched now. Only the walk at start and then
	// the event loop touch it.
	dirs     map[string]struct{}
	quit     chan struct{}
	done     chan struct{} // closed when the event loop has stopped
	errsDone chan struct{} // closed when the error loop has stopped
	once     sync.Once
}

// treeFsnotify is Tree with the fsnotify backend (inotify on Linux, kqueue on
// macOS).
func treeFsnotify(root string, onChange func(rel string, op Op), o Options) Watcher {
	s, failed := start(root, onChange, o)
	if failed != nil {
		return failed
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return stopped("cannot watch " + root + ": " + fsutil.ErrCode(err))
	}
	x := &fsnotifyWatcher{
		state:    s,
		w:        w,
		maxDirs:  o.MaxDirs,
		dirs:     map[string]struct{}{},
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
		errsDone: make(chan struct{}),
	}
	if x.maxDirs <= 0 {
		x.maxDirs = defaultMaxDirs
	}
	x.addTree(s.real, false)
	if _, ok := x.dirs[s.real]; !ok {
		_ = w.Close()
		return stopped(x.Degraded())
	}
	go x.loop()
	go x.errorLoop()
	return x
}

func (x *fsnotifyWatcher) Close() {
	x.once.Do(func() {
		x.closed.Store(true)
		close(x.quit)
		<-x.done
		_ = x.w.Close() // closes w.Errors
		<-x.errsDone
	})
}

// name is dir as messages show it: the root as the caller gave it.
func (x *fsnotifyWatcher) name(dir string) string {
	if dir == x.real {
		return x.root
	}
	return dir
}

// addTree watches dir and every directory below it. With report set, it
// reports each entry it finds as Rename.
func (x *fsnotifyWatcher) addTree(dir string, report bool) {
	if x.closed.Load() {
		return
	}
	if rel, _ := x.rel(dir); rel != "" && x.ignored(rel) {
		return
	}
	if _, ok := x.dirs[dir]; ok {
		return
	}
	if len(x.dirs) >= x.maxDirs {
		x.degrade(fmt.Sprintf("more than %d directories; later directories are not watched", x.maxDirs))
		return
	}
	if err := x.w.Add(dir); err != nil {
		x.fail("cannot watch", dir, err)
		return
	}
	x.dirs[dir] = struct{}{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		x.fail("cannot read", dir, err)
		return
	}
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		if report {
			if rel, ok := x.rel(child); ok {
				x.emit(rel, Rename)
			}
		}
		if e.IsDir() {
			x.addTree(child, report)
		}
	}
}

// fail degrades the watcher, except for a directory below the root that is
// gone by the time it is added or read: it has nothing left to watch.
func (x *fsnotifyWatcher) fail(what, dir string, err error) {
	if dir != x.real && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)) {
		return
	}
	x.degrade(what + " " + x.name(dir) + ": " + fsutil.ErrCode(err))
}

// forget drops dir and every watched directory below it. fsnotify has
// already dropped the watch of a removed or renamed directory; the watches
// below a renamed one are still there.
func (x *fsnotifyWatcher) forget(dir string) {
	if _, ok := x.dirs[dir]; !ok {
		return
	}
	prefix := dir + string(filepath.Separator)
	for d := range x.dirs {
		if d == dir || strings.HasPrefix(d, prefix) {
			delete(x.dirs, d)
			_ = x.w.Remove(d)
		}
	}
}

func (x *fsnotifyWatcher) loop() {
	defer close(x.done)
	for {
		select {
		case <-x.quit:
			return
		case ev, ok := <-x.w.Events:
			if !ok {
				return
			}
			x.handle(ev)
		}
	}
}

// errorLoop drains w.Errors on its own goroutine. fsnotify's inotify backend
// can send an error while it holds the lock that Add and Remove take, so the
// event loop must not be the only reader.
func (x *fsnotifyWatcher) errorLoop() {
	defer close(x.errsDone)
	for err := range x.w.Errors {
		if errors.Is(err, fsnotify.ErrEventOverflow) {
			x.degrade("events dropped under load")
		} else {
			x.degrade("watcher error on " + x.root + ": " + fsutil.ErrCode(err))
		}
	}
}

func (x *fsnotifyWatcher) handle(ev fsnotify.Event) {
	rel, ok := x.rel(ev.Name)
	if !ok {
		return
	}
	gone := ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
	if rel == "" {
		if gone {
			x.degrade("watcher error on " + x.root + ": ENOENT")
		}
		return
	}
	op := Change
	if gone || ev.Has(fsnotify.Create) {
		op = Rename
	}
	if gone {
		x.forget(ev.Name)
	}
	if op == Rename {
		if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
			x.addTree(ev.Name, true)
		}
	}
	x.emit(rel, op)
}
