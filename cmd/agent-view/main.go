// Command agent-view shows what the coding agents on this machine do, in a
// terminal. The collector, the remote hosts and the hub run in the same
// process as the TUI.
// "agent-view collect --stdio" runs the Go collector and writes NDJSON.
// "agent-view serve" runs the headless hub and "agent-view audit" checks it.
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
	"sync/atomic"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"agent-view/internal/bridge"
	"agent-view/internal/cli"
	"agent-view/internal/feed"
	"agent-view/internal/quiet"
	"agent-view/internal/store"
	"agent-view/internal/tui"
)

const notTerminal = "agent-view needs a terminal; use agent-view serve for a headless hub"

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

// diagWriter holds the diagnostics lines of the log package, the collector,
// the bridge, the stack and the captured stderr while the TUI runs. It queues
// lines without blocking and from any goroutine; once started, a goroutine
// hands them to the program, which owns the diagnostics buffer.
type diagWriter struct {
	ch   chan string
	done chan struct{}

	mu   sync.Mutex
	seq  uint64
	sent []diagLineMsg // the last lines handed to the program, oldest first

	seen atomic.Uint64 // the seq of the last line the program took
}

// diagLineMsg is one diagnostics line on its way to the program.
type diagLineMsg struct {
	seq  uint64
	text string
}

// diagCap is how many lines the queue and the sent list hold.
const diagCap = 256

func newDiagWriter() *diagWriter {
	return &diagWriter{ch: make(chan string, diagCap), done: make(chan struct{})}
}

func (w *diagWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		w.line(line)
	}
	return len(b), nil
}

// line queues one line. When the queue is full it drops the line rather than
// block the caller.
func (w *diagWriter) line(s string) {
	select {
	case w.ch <- s:
	default:
	}
}

// start sends the queued and later lines to send until ctx is done; then the
// lines stay in the queue.
func (w *diagWriter) start(ctx context.Context, send func(tea.Msg)) {
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-w.ch:
				w.mu.Lock()
				w.seq++
				m := diagLineMsg{seq: w.seq, text: line}
				w.sent = append(w.sent, m)
				if n := len(w.sent) - diagCap; n > 0 {
					w.sent = append(w.sent[:0:0], w.sent[n:]...)
				}
				w.mu.Unlock()
				send(m)
			}
		}
	}()
}

// filter is the program's message filter: it turns a diagLineMsg into a
// feed.DiagMsg and records that the program took it.
func (w *diagWriter) filter(_ tea.Model, msg tea.Msg) tea.Msg {
	if m, ok := msg.(diagLineMsg); ok {
		w.seen.Store(m.seq)
		return feed.DiagMsg(m.text)
	}
	return msg
}

// rest waits for the start goroutine to end and returns the lines that the
// program never took: lines sent as it ended, then the queue.
func (w *diagWriter) rest() []string {
	<-w.done
	var out []string
	seen := w.seen.Load()
	w.mu.Lock()
	for _, m := range w.sent {
		if m.seq > seen {
			out = append(out, m.text)
		}
	}
	w.mu.Unlock()
	for {
		select {
		case line := <-w.ch:
			out = append(out, line)
		default:
			return out
		}
	}
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
	switch args.Mode {
	case "collect":
		signals, stop := collectSignals()
		defer stop()
		return runCollect(args, stdout, stderr, signals)
	case "serve":
		signals, stop := collectSignals()
		defer stop()
		return runServe(args, stdout, stderr, signals)
	case "audit":
		return runAudit(args, stdout, stderr)
	}
	if !bothTerminals(stdin, stdout, term.IsTerminal) {
		fmt.Fprintln(stderr, notTerminal)
		return 2
	}
	return runTUI(args, stderr)
}

// runTUI runs the collector, the remotes and the hub in this process, and the
// TUI on them. Nothing writes to the terminal while the TUI runs: stderr and
// the log package go to the diagnostics, which print after the exit.
func runTUI(args cli.Args, stderr io.Writer) int {
	override, err := cli.RemoteCommandOverride(os.Getenv("AGENT_VIEW_REMOTE_COMMAND"))
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: %v\n", err)
		return 2
	}
	diag := newDiagWriter()
	st, err := newStack(args, override, diag.line)
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: %v\n", err)
		return 1
	}
	restore, err := quiet.CaptureStderr(diag.line)
	if err != nil {
		st.hub.Stop() // no source has started: this does not wait
		fmt.Fprintf(stderr, "agent-view: cannot capture stderr: %v\n", err)
		return 1
	}
	// On a panic the deferred calls stop the sources, then restore stderr,
	// so the runtime prints the panic on the real stderr.
	defer restore()
	log.SetOutput(diag)
	defer log.SetOutput(io.Discard)

	model := tui.NewModel(tui.Config{
		Store:   store.New(store.Options{LingerMs: args.LingerMs}),
		Reduced: args.Reduced, NoColor: noColor(), LingerMs: args.LingerMs,
	})
	p := tea.NewProgram(model, tea.WithFPS(20), tea.WithFilter(diag.filter))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	diag.start(ctx, p.Send)

	// The bridge subscribes before any source starts, so the first scan is
	// history in the store.
	br := bridge.Start(bridge.Config{Hub: st.hub, Send: p.Send, Diag: diag.line})
	shutdown := sync.OnceFunc(func() {
		br.Stop()
		st.stop(stopWait)
	})
	defer shutdown()
	st.start(diag.line)

	// Bubble Tea ends the program on SIGINT and SIGTERM; SIGHUP is ours.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		select {
		case <-ctx.Done():
		case <-hup:
			p.Quit()
		}
	}()

	_, runErr := p.Run()
	shutdown() // the program has ended: p.Send no longer blocks
	cancel()
	restore() // every captured line is queued when it returns
	late := diag.rest()

	for _, line := range model.Diagnostics() {
		fmt.Fprintln(stderr, line)
	}
	for _, line := range late {
		fmt.Fprintln(stderr, line)
	}
	if runErr != nil && !errors.Is(runErr, tea.ErrInterrupted) {
		fmt.Fprintf(stderr, "agent-view: %v\n", runErr)
		return 1
	}
	return 0
}
