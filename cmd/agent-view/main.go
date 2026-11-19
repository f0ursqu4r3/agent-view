// Command agent-view shows what the coding agents on this machine do, in a
// terminal. Phase 1 reads the TypeScript collector as a child process.
// "agent-view collect --stdio" runs the Go collector and writes NDJSON.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"agent-view/internal/cli"
	"agent-view/internal/feed"
	"agent-view/internal/store"
	"agent-view/internal/tui"
)

const notTerminal = "agent-view needs a terminal; agent-view serve comes in phase 2"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// bothTerminals reports whether stdin and stdout are terminals, as decided by
// isTTY. A character device such as /dev/null is not a terminal.
func bothTerminals(stdin, stdout *os.File, isTTY func(fd uintptr) bool) bool {
	return isTTY(stdin.Fd()) && isTTY(stdout.Fd())
}

// noColor reports whether NO_COLOR is set to a non-empty value. Any value
// turns color off, "0" and "false" too; Bubble Tea's own check reads it as a
// boolean, so the model encodes frames without color itself.
func noColor() bool { return os.Getenv("NO_COLOR") != "" }

// diagWriter is the output of the log package while the TUI runs. It queues
// lines without blocking; once started, a goroutine hands them to the
// program, which owns the diagnostics buffer.
type diagWriter struct {
	ch chan string
}

func newDiagWriter() *diagWriter { return &diagWriter{ch: make(chan string, 256)} }

func (w *diagWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		select {
		case w.ch <- line:
		default: // full: drop the line rather than block the logger
		}
	}
	return len(b), nil
}

// start sends the queued and later lines to send until ctx is done.
func (w *diagWriter) start(ctx context.Context, send func(tea.Msg)) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-w.ch:
				send(feed.DiagMsg(line))
			}
		}
	}()
}

// run is main without os.Exit; it returns the exit code.
func run(argv []string, stdin, stdout *os.File, stderr io.Writer) (code int) {
	args, err := cli.Parse(argv)
	if err != nil {
		var ue cli.UsageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "agent-view: %s\n%s\n", ue.Msg, cli.Usage)
			return 2
		}
		fmt.Fprintf(stderr, "agent-view: %v\n", err)
		return 1
	}
	if args.Mode == "collect" {
		signals, stop := collectSignals()
		defer stop()
		return runCollect(args, stdout, stderr, signals)
	}
	if !bothTerminals(stdin, stdout, term.IsTerminal) {
		fmt.Fprintln(stderr, notTerminal)
		return 2
	}
	collector, err := feed.DefaultArgv(args.CollectorFlags())
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: %v\n", err)
		return 2
	}

	// Nothing may write to the terminal while the TUI runs.
	diag := newDiagWriter()
	log.SetOutput(diag)

	st := store.New(store.Options{LingerMs: args.LingerMs})
	model := tui.NewModel(tui.Config{
		Store: st, Reduced: args.Reduced, NoColor: noColor(), LingerMs: args.LingerMs,
	})
	p := tea.NewProgram(model, tea.WithFPS(20))

	ctx, cancel := context.WithCancel(context.Background())
	diag.start(ctx, p.Send)

	// The feed runs in its own goroutine: p.Send blocks until the event loop
	// runs. The deferred stop also runs on a panic, so the child never stays.
	var feedDone sync.WaitGroup
	feedDone.Add(1)
	go func() {
		defer feedDone.Done()
		feed.Run(ctx, feed.Config{Argv: collector, Send: p.Send})
	}()
	stopFeed := sync.OnceFunc(func() {
		cancel()
		feedDone.Wait()
	})
	defer stopFeed()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				p.Quit()
			}
		}
	}()

	_, runErr := p.Run()
	stopFeed()
	log.SetOutput(io.Discard)

	for _, line := range model.Diagnostics() {
		fmt.Fprintln(stderr, line)
	}
	if runErr != nil && !errors.Is(runErr, tea.ErrInterrupted) {
		fmt.Fprintf(stderr, "agent-view: %v\n", runErr)
		return 1
	}
	return 0
}
