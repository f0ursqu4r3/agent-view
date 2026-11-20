package main

import (
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// shutdownSignals is the one handler of SIGINT, SIGTERM and SIGHUP for the
// whole TUI run. While it is registered, none of the three gets its default
// action: the first closes Requested, later ones are read and ignored, so a
// second Ctrl-C during the shutdown cannot end the process before the held
// diagnostics print.
type shutdownSignals struct {
	ch        chan os.Signal
	requested chan struct{}
	quit      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	read      atomic.Uint64 // signals read so far
}

// watchShutdownSignals registers SIGINT, SIGTERM and SIGHUP on one channel and
// starts the goroutine that reads it.
func watchShutdownSignals() *shutdownSignals {
	s := &shutdownSignals{
		ch:        make(chan os.Signal, 4),
		requested: make(chan struct{}),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	signal.Notify(s.ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go s.loop()
	return s
}

func (s *shutdownSignals) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.quit:
			return
		case <-s.ch:
			if s.read.Add(1) == 1 {
				close(s.requested)
			}
		}
	}
}

// Requested is closed when the first signal arrives.
func (s *shutdownSignals) Requested() <-chan struct{} { return s.requested }

// Stop unregisters the signals and ends the goroutine; the three signals get
// their default action again. A second call does nothing.
func (s *shutdownSignals) Stop() {
	s.stopOnce.Do(func() {
		signal.Stop(s.ch)
		close(s.quit)
		<-s.done
	})
}
