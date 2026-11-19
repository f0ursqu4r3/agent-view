package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"agent-view/internal/cli"
	"agent-view/internal/collect"
	"agent-view/internal/lifecycle"
	"agent-view/internal/wire"
)

// stopWait is how long runCollect waits for Collector.Stop. Stop waits for a
// log scan or a 4 MiB read in flight, and for a subscriber blocked on a
// full stdout pipe; the process must exit within 2 s.
const stopWait = 1500 * time.Millisecond

// collectSignals returns a channel of the signals that stop the collector
// (SIGINT, SIGTERM, SIGHUP) and a function that undoes the registration.
// It also registers SIGPIPE, so a write to a closed stdout returns an error
// instead of killing the process; that signal is not sent to the channel.
func collectSignals() (<-chan os.Signal, func()) {
	stopSigs := make(chan os.Signal, 4)
	signal.Notify(stopSigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	pipe := make(chan os.Signal, 1) // never read: Notify never blocks
	signal.Notify(pipe, syscall.SIGPIPE)
	return stopSigs, func() {
		signal.Stop(stopSigs)
		signal.Stop(pipe)
	}
}

// runCollect ports collectStdio of the former TypeScript cli.ts (removed in
// P2c; see git history). It writes one NDJSON envelope per line to stdout
// until a signal arrives on signals or a write fails, and
// returns the exit code: 0 after a stop request, 1 when the collector cannot
// start.
func runCollect(args cli.Args, stdout io.Writer, stderr io.Writer, signals <-chan os.Signal) int {
	host := args.Label
	if host == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "localhost"
		}
		host = h
	}
	c := collect.New(collect.Options{
		Host:      host,
		Demo:      args.Demo,
		DemoScale: args.DemoScale,
		Watch:     args.Watch,
		Lifecycle: lifecycle.Options{LingerMs: args.LingerMs},
	})

	var closing atomic.Bool
	writeFailed := make(chan struct{}, 1)
	// The subscriber runs on the collector's loop goroutine. It must not call
	// Stop; on a write error it tells this goroutine to do so.
	write := func(env wire.Envelope) {
		if closing.Load() {
			return
		}
		if _, err := stdout.Write(wire.EncodeLine(env)); err != nil {
			closing.Store(true)
			select {
			case writeFailed <- struct{}{}:
			default:
			}
		}
	}

	// Subscribe waits until hello and snapshot are written, and Start waits
	// for the first scan, so both run off this goroutine: a signal must be
	// able to end the process while either is blocked.
	started := make(chan error, 1)
	go func() {
		c.Subscribe(write)
		started <- c.Start()
	}()

	code := 0
	for done := false; !done; {
		select {
		case <-signals:
			done = true
		case <-writeFailed:
			done = true
		case err := <-started:
			if err != nil {
				fmt.Fprintf(stderr, "agent-view: collector failed: %v\n", err)
				code = 1
				done = true
			} else {
				started = nil // started: wait for a stop request only
			}
		}
	}

	closing.Store(true)
	stopped := make(chan struct{})
	go func() {
		c.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(stopWait):
	}
	return code
}
