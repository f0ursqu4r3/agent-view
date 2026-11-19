//go:build darwin

package main

import (
	"strings"

	"github.com/fsnotify/fsevents"
)

type fsevWatcher struct {
	es   *fsevents.EventStream
	quit chan struct{}
	done chan struct{}
}

func newFSEvents(root string, cb func(string), o wopts, st *wstats) (watcher, error) {
	flags := fsevents.FileEvents
	if o.noDefer {
		flags |= fsevents.NoDefer
	}
	es := &fsevents.EventStream{
		Paths:   []string{root},
		Latency: o.latency,
		Flags:   flags,
		Events:  make(chan []fsevents.Event, 1024),
	}
	if err := es.Start(); err != nil {
		return nil, err
	}
	w := &fsevWatcher{es: es, quit: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case batch := <-es.Events:
				for _, e := range batch {
					if e.Flags&fsevents.MustScanSubDirs != 0 {
						st.mustScan.Add(1)
					}
					if e.Flags&fsevents.UserDropped != 0 {
						st.userDropped.Add(1)
					}
					if e.Flags&fsevents.KernelDropped != 0 {
						st.kernelDropped.Add(1)
					}
					if e.Flags&fsevents.HistoryDone != 0 {
						continue
					}
					p := e.Path
					if !strings.HasPrefix(p, "/") {
						p = "/" + p
					}
					if e.Flags&(fsevents.MustScanSubDirs|fsevents.UserDropped|fsevents.KernelDropped) != 0 {
						st.addDropPath(p)
					}
					cb(p)
				}
			case <-w.quit:
				return
			}
		}
	}()
	return w, nil
}

func (w *fsevWatcher) Close() {
	w.es.Stop()
	close(w.quit)
	<-w.done
}
