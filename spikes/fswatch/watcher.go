package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ignoredDirs is IGNORED_DIRS from the former TypeScript
// collector/sources/fs-changes.ts (removed in P2c; see git history).
var ignoredDirs = map[string]bool{
	".git": true, ".worktrees": true, ".pytest_cache": true, "__pycache__": true, ".turbo": true,
	"coverage": true, "node_modules": true, ".next": true, ".cache": true, ".venv": true, "venv": true,
	"dist": true, "build": true, ".serena": true, ".superpowers": true, "target": true, "out": true,
}

type wopts struct {
	latency time.Duration // FSEvents latency
	noDefer bool          // FSEvents kFSEventStreamCreateFlagNoDefer
	maxDirs int           // fsnotify: directory cap, as MAX_DIRS in fs-changes.ts
}

type wstats struct {
	mustScan, userDropped, kernelDropped atomic.Int64 // FSEvents flags
	errors, overflow, addErrors          atomic.Int64 // fsnotify
	dirs                                 atomic.Int64 // fsnotify: watched directories
	capped                               atomic.Bool

	mu        sync.Mutex
	dropPaths []string // FSEvents: paths of events with a drop or rescan flag
}

func (st *wstats) addDropPath(p string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.dropPaths) < 20 {
		st.dropPaths = append(st.dropPaths, p)
	}
}

type watcher interface{ Close() }

// newWatcher watches root (already resolved with EvalSymlinks) and calls cb with
// the absolute path of every change. cb may run on another goroutine.
func newWatcher(backend, root string, cb func(abs string), o wopts, st *wstats) (watcher, error) {
	switch backend {
	case "fsevents":
		return newFSEvents(root, cb, o, st)
	case "fsnotify":
		return newFsnotify(root, cb, o, st)
	}
	return nil, errors.New("unknown backend " + backend)
}

// fsnWatcher walks the tree and adds one fsnotify watch per directory
// (kqueue also opens one descriptor per file). New directories are added on
// Create, then read, so files written before the add are still reported.
type fsnWatcher struct {
	w    *fsnotify.Watcher
	cb   func(string)
	o    wopts
	st   *wstats
	quit chan struct{}
	done chan struct{}
}

func newFsnotify(root string, cb func(string), o wopts, st *wstats) (watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	x := &fsnWatcher{w: w, cb: cb, o: o, st: st, quit: make(chan struct{}), done: make(chan struct{})}
	x.addTree(root, false)
	go x.loop()
	return x, nil
}

func (x *fsnWatcher) addTree(dir string, report bool) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != dir && ignoredDirs[d.Name()] {
				return fs.SkipDir
			}
			if x.o.maxDirs > 0 && int(x.st.dirs.Load()) >= x.o.maxDirs {
				x.st.capped.Store(true)
				return fs.SkipAll
			}
			if err := x.w.Add(p); err != nil {
				x.st.addErrors.Add(1)
				return fs.SkipDir
			}
			x.st.dirs.Add(1)
		}
		if report && p != dir {
			x.cb(p)
		}
		return nil
	})
}

func (x *fsnWatcher) loop() {
	defer close(x.done)
	for {
		select {
		case ev, ok := <-x.w.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Create) && !ignoredDirs[filepath.Base(ev.Name)] {
				if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
					x.addTree(ev.Name, true)
				}
			}
			x.cb(ev.Name)
		case err, ok := <-x.w.Errors:
			if !ok {
				return
			}
			x.st.errors.Add(1)
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				x.st.overflow.Add(1)
			}
		case <-x.quit:
			return
		}
	}
}

func (x *fsnWatcher) Close() {
	close(x.quit)
	<-x.done
	_ = x.w.Close()
}

// openFDs counts this process's open descriptors (minus the one ReadDir uses).
func openFDs() int {
	for _, d := range []string{"/proc/self/fd", "/dev/fd"} {
		if es, err := os.ReadDir(d); err == nil {
			return len(es) - 1
		}
	}
	return -1
}

// cpuTime is user plus system CPU time of this process.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// maxRSSMB is the peak resident set size in MB.
func maxRSSMB() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / (1 << 20)
	}
	return float64(ru.Maxrss) / (1 << 10)
}

// recorder keeps the first event time per path relative to root.
type recorder struct {
	mu      sync.Mutex
	root    string
	prefix  string
	first   map[string]time.Time
	events  int
	foreign int
}

func newRecorder(realRoot string) *recorder {
	return &recorder{root: realRoot, prefix: realRoot + string(filepath.Separator), first: map[string]time.Time{}}
}

func (r *recorder) onEvent(abs string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if abs == r.root {
		r.events++
		return
	}
	if len(abs) <= len(r.prefix) || abs[:len(r.prefix)] != r.prefix {
		r.foreign++
		return
	}
	r.events++
	rel := abs[len(r.prefix):]
	if _, ok := r.first[rel]; !ok {
		r.first[rel] = now
	}
}
