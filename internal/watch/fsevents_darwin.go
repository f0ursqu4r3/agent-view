package watch

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"

	"agent-view/internal/fsutil"
)

// native is the backend of Tree on macOS.
func native(root string, onChange func(rel string, op Op), o Options) Watcher {
	return treeFSEvents(root, onChange, o)
}

const (
	fseventsFlags   = fsevents.FileEvents | fsevents.NoDefer | fsevents.WatchRoot
	fseventsLatency = 50 * time.Millisecond
	// fseventsDrops are the flags of an event that stands for lost events.
	fseventsDrops = fsevents.MustScanSubDirs | fsevents.UserDropped | fsevents.KernelDropped
)

// fseventsWatcher is one FSEvents stream on the root. It cannot skip ignored
// directories, so the event loop filters them.
type fseventsWatcher struct {
	*state
	es   *fsevents.EventStream
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

func treeFSEvents(root string, onChange func(rel string, op Op), o Options) Watcher {
	s, failed := start(root, onChange, o)
	if failed != nil {
		return failed
	}
	es := &fsevents.EventStream{
		Paths:   []string{s.real},
		Latency: fseventsLatency,
		Flags:   fseventsFlags,
		Events:  make(chan []fsevents.Event, 1024),
	}
	if err := es.Start(); err != nil {
		return stopped("cannot watch " + root + ": " + fsutil.ErrCode(err))
	}
	x := &fseventsWatcher{state: s, es: es, quit: make(chan struct{}), done: make(chan struct{})}
	go x.loop()
	return x
}

func (x *fseventsWatcher) Close() {
	x.once.Do(func() {
		x.closed.Store(true)
		// The loop keeps draining while the stream stops, so a callback
		// that is sending a batch is not blocked.
		x.es.Stop()
		close(x.quit)
		<-x.done
	})
}

func (x *fseventsWatcher) loop() {
	defer close(x.done)
	for {
		select {
		case <-x.quit:
			return
		case batch := <-x.es.Events:
			x.handle(batch)
		}
	}
}

// handle reports a batch. Every event reports Rename, as Bun's fs.watch does
// on macOS.
func (x *fseventsWatcher) handle(batch []fsevents.Event) {
	for _, e := range batch {
		switch {
		case e.Flags&fseventsDrops != 0:
			x.degrade("events dropped under load")
			continue
		case e.Flags&fsevents.RootChanged != 0:
			if _, err := os.Stat(x.real); err != nil {
				x.degrade("watcher error on " + x.root + ": " + fsutil.ErrCode(err))
			}
			continue
		case e.Flags&fsevents.HistoryDone != 0:
			continue
		}
		p := e.Path
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if rel, ok := x.rel(p); ok && rel != "" {
			x.emit(rel, Rename)
		}
	}
}
