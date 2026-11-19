//go:build darwin || linux

package feed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/schema"
)

const (
	defaultBatch      = 50 * time.Millisecond
	defaultMinBackoff = time.Second
	defaultMaxBackoff = 30 * time.Second
	// defaultResetAfter is how long a child must stay up for the restart
	// delay to start over at MinBackoff.
	defaultResetAfter = 60 * time.Second
	// defaultStopGrace is how long a child has to exit after SIGTERM before
	// its process group gets SIGKILL.
	defaultStopGrace = 2 * time.Second
	// malformedDiagEvery is the number of skipped lines per diagnostics line.
	malformedDiagEvery = 100
)

// Config configures Run. Zero values select the defaults.
type Config struct {
	// Argv is the child command. Argv[0] is the program.
	Argv []string
	// Dir is the working directory of the child. Empty means the current one.
	Dir string
	// Send delivers messages to the TUI; in main it is (*tea.Program).Send.
	Send func(tea.Msg)
	// Batch is the shortest time between two EnvelopesMsg (default 50 ms).
	Batch time.Duration
	// MinBackoff and MaxBackoff bound the delay before a restart
	// (defaults 1 s and 30 s). The delay doubles after each restart.
	MinBackoff, MaxBackoff time.Duration

	// resetAfter and stopGrace are test hooks; zero selects the defaults.
	resetAfter time.Duration
	stopGrace  time.Duration
}

func (c Config) withDefaults() Config {
	if c.Send == nil {
		c.Send = func(tea.Msg) {}
	}
	if c.Batch <= 0 {
		c.Batch = defaultBatch
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = defaultMinBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.MaxBackoff < c.MinBackoff {
		c.MaxBackoff = c.MinBackoff
	}
	if c.resetAfter <= 0 {
		c.resetAfter = defaultResetAfter
	}
	if c.stopGrace <= 0 {
		c.stopGrace = defaultStopGrace
	}
	return c
}

// Run starts the collector child and feeds its output to c.Send until ctx is
// done. When the child exits, Run reports it as down, waits, and starts it
// again. Run returns after the child is reaped and every reader goroutine has
// finished. A deliberate stop through ctx sends no down message.
func Run(ctx context.Context, c Config) {
	c = c.withDefaults()
	if len(c.Argv) == 0 {
		c.Send(StateMsg{State: "down", Detail: "no collector command"})
		return
	}
	r := &runner{c: c}
	backoff := c.MinBackoff
	for ctx.Err() == nil {
		upFor := r.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		wait, backoff = nextBackoff(backoff, c.MinBackoff, c.MaxBackoff, upFor, c.resetAfter)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// nextBackoff returns the delay before the next start and the delay after
// that. A child that stayed up for resetAfter starts the sequence over.
func nextBackoff(cur, minB, maxB, upFor, resetAfter time.Duration) (wait, next time.Duration) {
	if upFor >= resetAfter {
		cur = minB
	}
	next = cur * 2
	if next > maxB {
		next = maxB
	}
	return cur, next
}

// runner holds state that outlives one child.
type runner struct {
	c         Config
	host      string // host label of the last envelope seen
	malformed int    // lines that did not decode, over all children
}

// runOnce runs one child to its end. It returns how long the child was up.
//
// The child leads its own process group. The pipes are plain files, not
// exec's StdoutPipe, so the child can be reaped while its output is still
// being read: once the child exits, the rest of its group is killed, so no
// leftover process survives it, and a leftover that held the pipes no
// longer keeps them open.
func (r *runner) runOnce(ctx context.Context) time.Duration {
	c := r.c
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, outW, err := os.Pipe()
	if err != nil {
		r.down(err.Error())
		return 0
	}
	defer stdout.Close()
	stderr, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		r.down(err.Error())
		return 0
	}
	defer stderr.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	started := time.Now()
	err = cmd.Start()
	// The child holds its own copies; the reads end when every holder closes.
	outW.Close()
	errW.Close()
	if err != nil {
		r.down(err.Error())
		return 0
	}
	pid := cmd.Process.Pid

	// Reaper: wait for the child, then kill what is left of its group.
	// After the reap the group ID stays in use while any member lives, so
	// the kill reaches only them; ESRCH (no member left) is ignored.
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		killGroup(pid, syscall.SIGKILL)
		close(exited)
	}()

	// Terminator: on ctx done, SIGTERM the group, then SIGKILL it after the
	// grace period. It ends when the child is reaped.
	var termDone sync.WaitGroup
	termDone.Add(1)
	go func() {
		defer termDone.Done()
		select {
		case <-exited:
		case <-ctx.Done():
			killGroup(pid, syscall.SIGTERM)
			grace := time.NewTimer(c.stopGrace)
			defer grace.Stop()
			select {
			case <-exited:
			case <-grace.C:
				killGroup(pid, syscall.SIGKILL)
				<-exited
			}
		}
	}()

	// Stderr reader: each line is a diagnostics line; the last one goes into
	// the down detail.
	var readers sync.WaitGroup
	var errMu sync.Mutex
	var lastErr string
	readers.Add(1)
	go func() {
		defer readers.Done()
		readLines(stderr, func(line []byte) {
			s := strings.TrimRight(string(line), "\r\n")
			if s == "" {
				return
			}
			errMu.Lock()
			lastErr = s
			errMu.Unlock()
			c.Send(DiagMsg(s))
		})
	}()

	// Stdout reader: decode lines and hand envelopes to this goroutine.
	envs := make(chan schema.Envelope, 256)
	readers.Add(1)
	go func() {
		defer readers.Done()
		defer close(envs)
		readLines(stdout, func(line []byte) {
			if len(bytes.TrimSpace(line)) == 0 {
				return
			}
			e, ok := schema.DecodeLine(line)
			if !ok {
				r.skipped()
				return
			}
			envs <- e
		})
	}()

	r.batch(envs)

	// Stdout is at EOF here: the child and its group are gone, or the child
	// closed it. Wait for the stderr reader and the reap.
	readers.Wait()
	<-exited
	termDone.Wait()
	upFor := time.Since(started)
	if ctx.Err() != nil {
		return upFor
	}
	errMu.Lock()
	last := lastErr
	errMu.Unlock()
	detail := "exit status 0"
	if waitErr != nil {
		detail = waitErr.Error()
	}
	if last != "" {
		detail += " — " + last
	}
	r.down(detail)
	return upFor
}

// batch reads envelopes until the channel closes. It sends StateMsg up on the
// first envelope and at most one EnvelopesMsg per Batch interval. It flushes
// the pending batch before it returns.
func (r *runner) batch(envs <-chan schema.Envelope) {
	var pending []schema.Envelope
	var timer *time.Timer
	var tick <-chan time.Time
	up := false
	flush := func() {
		if timer != nil {
			timer.Stop()
			timer, tick = nil, nil
		}
		if len(pending) > 0 {
			r.c.Send(EnvelopesMsg(pending))
			pending = nil
		}
	}
	for {
		select {
		case e, ok := <-envs:
			if !ok {
				flush()
				return
			}
			if !up {
				up = true
				if e.Host != "" {
					r.host = e.Host
				}
				r.c.Send(StateMsg{Host: e.Host, State: "up"})
			}
			pending = append(pending, e)
			if tick == nil {
				timer = time.NewTimer(r.c.Batch)
				tick = timer.C
			}
		case <-tick:
			flush()
		}
	}
}

func (r *runner) down(detail string) {
	r.c.Send(StateMsg{Host: r.host, State: "down", Detail: detail})
}

func (r *runner) skipped() {
	r.malformed++
	if r.malformed%malformedDiagEvery == 0 {
		r.c.Send(DiagMsg(fmt.Sprintf("skipped %d malformed lines", r.malformed)))
	}
}

// readLines calls fn for every line of rd, including a last line without a
// newline. There is no line length limit.
func readLines(rd io.Reader, fn func([]byte)) {
	br := bufio.NewReaderSize(rd, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fn(line)
		}
		if err != nil {
			return
		}
	}
}

// killGroup sends sig to the process group of pid. An error (ESRCH when no
// member is left) is ignored.
func killGroup(pid int, sig syscall.Signal) {
	_ = syscall.Kill(-pid, sig)
}

// DefaultArgv returns the collector command. AGENT_VIEW_COLLECTOR (a JSON
// array of non-empty strings) replaces it as a whole, flags included. Without
// it, the command is "bun src/cli.ts collect --stdio" plus flags, and the
// current directory must be the repository root.
func DefaultArgv(flags []string) ([]string, error) {
	if v := os.Getenv("AGENT_VIEW_COLLECTOR"); v != "" {
		var argv []string
		if err := json.Unmarshal([]byte(v), &argv); err != nil {
			return nil, fmt.Errorf("AGENT_VIEW_COLLECTOR must be a JSON array of strings: %w", err)
		}
		if len(argv) == 0 {
			return nil, errors.New("AGENT_VIEW_COLLECTOR must not be an empty array")
		}
		for _, a := range argv {
			if a == "" {
				return nil, errors.New("AGENT_VIEW_COLLECTOR must not contain an empty string")
			}
		}
		return argv, nil
	}
	if _, err := os.Stat("src/cli.ts"); err != nil {
		return nil, errors.New("run from the agent-view repository root, or set AGENT_VIEW_COLLECTOR")
	}
	argv := []string{"bun", "src/cli.ts", "collect", "--stdio"}
	return append(argv, flags...), nil
}
