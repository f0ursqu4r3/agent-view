//go:build darwin || linux

// Package remote runs a collector on another machine over SSH and makes it a
// hub source. It ports src/hub/remote.ts.
package remote

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"agent-view/internal/logs"
	"agent-view/internal/text"
	"agent-view/internal/wire"
)

const (
	// defaultStableUp is how long a child must stay up after its hello for
	// the retry delay to start over.
	defaultStableUp = 60 * time.Second
	// killAfter is how long a child has after SIGTERM before its process
	// group gets SIGKILL. It is also how long the readers have to reach the
	// end of the pipes after the child is reaped.
	killAfter = 2 * time.Second
	// maxDetail is the longest status detail, in UTF-16 units.
	maxDetail = 200
	// maxStderrPending is how much of an incomplete stderr line is kept, in
	// UTF-16 units: its end.
	maxStderrPending = 4096
	// defaultMaxLineBytes is the longest child stdout line kept.
	defaultMaxLineBytes = 8 << 20
	defaultFirstDelayMs = 1000
	defaultMaxDelayMs   = 30000
	readSize            = 64 << 10
)

// sshHostRE accepts host names and user@host: no leading "-" (ssh would read
// an option), no spaces, no shell characters.
var sshHostRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@:-]*$`)

// ValidSSHHost reports whether s is a host name or user@host that is safe to
// pass to ssh.
func ValidSSHHost(s string) bool { return sshHostRE.MatchString(s) }

// shellQuote quotes s for a POSIX remote shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// DefaultCommand is the child command: agent-view collect --stdio over ssh.
// The remote shell reads the label, so it is quoted.
func DefaultCommand(sshHost, label string) []string {
	return []string{"ssh", "-T", "-o", "BatchMode=yes", "-o", "ServerAliveInterval=15", sshHost,
		"agent-view", "collect", "--stdio", "--label", shellQuote(label)}
}

// Options configure a Remote.
type Options struct {
	// SSHHost is the ssh destination: a host name or user@host.
	SSHHost string
	// Label is the host label.
	Label string
	// Command builds the child command. Nil means DefaultCommand.
	Command func(sshHost, label string) []string
	// BackoffMs is the first and the longest retry delay, in ms. A zero field
	// takes its default: 1,000 and 30,000.
	BackoffMs [2]int64
	// MaxLineBytes is the longest child stdout line kept. A longer line is
	// dropped and counted as malformed. Zero means 8 MiB.
	MaxLineBytes int
}

// Remote is a collector on another machine. It runs the child command (by
// default agent-view collect --stdio over ssh), reads NDJSON envelopes from
// the child's stdout, and starts the child again with backoff when it exits.
// Each new child is a fresh process: its hello and snapshot flow through
// Subscribe like any other envelope.
//
// The status is connecting while a child starts, up after its first hello,
// down with a detail after it exits or fails to start, and down after Stop.
// Subscribers and status listeners run without the Remote's lock; calls to
// subscribers never overlap, and neither do calls to listeners. They must not
// call Stop, which waits for the goroutine that calls them.
type Remote struct {
	sshHost      string
	label        string
	command      func(sshHost, label string) []string
	firstDelay   time.Duration
	maxDelay     time.Duration
	maxLineBytes int
	// stableUp is defaultStableUp; tests shorten it before Start.
	stableUp time.Duration

	malformed atomic.Int64

	mu        sync.Mutex
	state     string
	detail    string
	subs      []*subscriber
	listeners []*listener
	started   bool
	stopped   bool
	stopCh    chan struct{} // closed by the first Stop
	done      chan struct{} // closed when the loop has ended
}

type subscriber struct {
	fn      func(wire.Envelope)
	removed atomic.Bool
}

type listener struct{ fn func() }

// New returns a Remote for o. It fails when o.SSHHost is not a valid ssh
// host. It does not start the child.
func New(o Options) (*Remote, error) {
	if !ValidSSHHost(o.SSHHost) {
		return nil, errors.New("invalid ssh host: use a host name or user@host")
	}
	r := &Remote{
		sshHost:      o.SSHHost,
		label:        o.Label,
		command:      o.Command,
		firstDelay:   time.Duration(o.BackoffMs[0]) * time.Millisecond,
		maxDelay:     time.Duration(o.BackoffMs[1]) * time.Millisecond,
		maxLineBytes: o.MaxLineBytes,
		stableUp:     defaultStableUp,
		state:        "connecting",
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
	}
	if r.command == nil {
		r.command = DefaultCommand
	}
	if r.firstDelay <= 0 {
		r.firstDelay = defaultFirstDelayMs * time.Millisecond
	}
	if r.maxDelay <= 0 {
		r.maxDelay = defaultMaxDelayMs * time.Millisecond
	}
	if r.maxLineBytes <= 0 {
		r.maxLineBytes = defaultMaxLineBytes
	}
	return r, nil
}

// Label returns the host label.
func (r *Remote) Label() string { return r.label }

// Kind returns "remote".
func (r *Remote) Kind() string { return "remote" }

// MalformedLines counts the child lines that were not valid envelopes. They
// are skipped.
func (r *Remote) MalformedLines() int { return int(r.malformed.Load()) }

// Subscribe calls fn with every envelope of every child, on the goroutine
// that reads the child's stdout. A panic in fn is recovered.
func (r *Remote) Subscribe(fn func(wire.Envelope)) (unsubscribe func()) {
	s := &subscriber{fn: fn}
	r.mu.Lock()
	r.subs = append(r.subs, s)
	r.mu.Unlock()
	return func() {
		s.removed.Store(true)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.subs = slices.DeleteFunc(r.subs, func(x *subscriber) bool { return x == s })
	}
}

// Status returns the host status.
func (r *Remote) Status() wire.HostStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return wire.HostStatus{Label: r.label, Kind: "remote", State: r.state, Detail: r.detail}
}

// OnStatusChange calls fn after every change of the state or the detail,
// without the Remote's lock. A panic in fn is recovered.
func (r *Remote) OnStatusChange(fn func()) (remove func()) {
	l := &listener{fn: fn}
	r.mu.Lock()
	r.listeners = append(r.listeners, l)
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.listeners = slices.DeleteFunc(r.listeners, func(x *listener) bool { return x == l })
	}
}

// Start starts the first child. It does not wait for the connection, and it
// does nothing after Start or Stop.
func (r *Remote) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return nil
	}
	r.started = true
	go r.loop()
	return nil
}

// Stop cancels the retry and ends the child: SIGTERM to its process group,
// SIGKILL to the group 2 s later. It waits for the child and the readers, and
// it never starts a child again. The status is down after it. A second call
// waits like the first.
func (r *Remote) Stop() {
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
		close(r.stopCh)
	}
	started := r.started
	r.mu.Unlock()
	if started {
		<-r.done
	}
	r.change("down", "", true)
}

// loop runs children until Stop. The retry delay starts at firstDelay and
// doubles after each exit, up to maxDelay; a child that stays up for stableUp
// after its hello starts it over.
func (r *Remote) loop() {
	defer close(r.done)
	delay := r.firstDelay
	for r.change("connecting", "", false) {
		detail := r.run(&delay)
		if !r.change("down", text.SliceUTF16(detail, maxDetail), false) {
			return
		}
		wait := delay
		delay = min(delay*2, r.maxDelay)
		t := time.NewTimer(wait)
		select {
		case <-r.stopCh:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// run runs one child to its end and returns the down detail. It sets *delay
// to firstDelay when the child stays up for stableUp after its hello.
//
// The child leads its own process group, and its stdin is /dev/null. Once the
// child is reaped, what is left of its group gets SIGKILL. A process outside
// the group can still hold the pipes: killAfter after the reap, the read ends
// are closed.
func (r *Remote) run(delay *time.Duration) string {
	c, err := spawn(r.command(r.sshHost, r.label))
	if err != nil {
		return "spawn failed: " + err.Error()
	}
	defer c.stdout.Close()
	defer c.stderr.Close()
	pid := c.cmd.Process.Pid

	exited := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		killGroup(pid, syscall.SIGKILL)
		close(exited)
	}()

	hello := make(chan struct{})
	finished := make(chan struct{}, 2)
	var lastStderr string
	go func() {
		defer func() { finished <- struct{}{} }()
		r.readStdout(c.stdout, hello)
	}()
	go func() {
		defer func() { finished <- struct{}{} }()
		lastStderr = readStderr(c.stderr)
	}()

	var timers []*time.Timer
	arm := func(d time.Duration) <-chan time.Time {
		t := time.NewTimer(d)
		timers = append(timers, t)
		return t.C
	}
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()
	var (
		exitedC   <-chan struct{} = exited
		finishedC <-chan struct{} = finished
		helloC    <-chan struct{} = hello
		stopC     <-chan struct{} = r.stopCh

		stableC, killC, graceC <-chan time.Time
	)
	for open := 2; exitedC != nil || open > 0; {
		select {
		case <-exitedC:
			exitedC, stopC, killC = nil, nil, nil
			if open > 0 {
				graceC = arm(killAfter)
			}
		case <-finishedC:
			if open--; open == 0 {
				finishedC, graceC = nil, nil
			}
		case <-helloC:
			helloC = nil
			stableC = arm(r.stableUp)
		case <-stableC:
			stableC = nil
			*delay = r.firstDelay
		case <-stopC:
			stopC = nil
			killGroup(pid, syscall.SIGTERM)
			killC = arm(killAfter)
		case <-killC:
			killC = nil
			killGroup(pid, syscall.SIGKILL)
		case <-graceC:
			graceC = nil
			_ = c.stdout.Close()
			_ = c.stderr.Close()
		}
	}
	return r.exitDetail(c.cmd.ProcessState, lastStderr)
}

// exitDetail describes how the child ended.
func (r *Remote) exitDetail(ps *os.ProcessState, lastStderr string) string {
	what := "unknown"
	if ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			switch {
			case ws.Exited() && ws.ExitStatus() == 127:
				return "agent-view not found on " + r.sshHost
			case ws.Exited() && ws.ExitStatus() == 255:
				return "ssh failed: " + lastStderr
			case ws.Exited():
				what = strconv.Itoa(ws.ExitStatus())
			case ws.Signaled():
				if name := unix.SignalName(ws.Signal()); name != "" {
					what = name
				}
			}
		}
	}
	return "collector exited " + what
}

// child is a started child with the read ends of its stdout and stderr.
type child struct {
	cmd            *exec.Cmd
	stdout, stderr *os.File
}

// spawn starts argv as the leader of a new process group. The pipes are plain
// files, not exec's StdoutPipe, so the child can be reaped while its output is
// still being read.
func spawn(argv []string) (*child, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderr, errW, err := os.Pipe()
	if err != nil {
		stdout.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	// The child holds its own copies; the reads end when every holder closes.
	outW.Close()
	errW.Close()
	if err != nil {
		stdout.Close()
		stderr.Close()
		return nil, err
	}
	return &child{cmd: cmd, stdout: stdout, stderr: stderr}, nil
}

// killGroup sends sig to the process group of pid. An error (ESRCH when no
// member is left) is ignored.
func killGroup(pid int, sig syscall.Signal) { _ = syscall.Kill(-pid, sig) }

// readStdout delivers each envelope line of f until the end of f, and closes
// hello after the first hello that set the status up. A line that is not an
// envelope is counted and skipped. A line longer than maxLineBytes is counted
// once and dropped up to and including its newline. An incomplete last line
// is dropped.
func (r *Remote) readStdout(f *os.File, hello chan<- struct{}) {
	var split logs.LineSplitter
	buf := make([]byte, readSize)
	discarding, sawHello := false, false
	for {
		n, err := f.Read(buf)
		chunk := buf[:n]
		if discarding && n > 0 {
			if nl := bytes.IndexByte(chunk, '\n'); nl < 0 {
				chunk = nil
			} else {
				chunk, discarding = chunk[nl+1:], false
			}
		}
		if len(chunk) > 0 {
			for _, line := range split.Push(chunk) {
				if jsTrim(line) == "" {
					continue
				}
				env, err := wire.DecodeEnvelope([]byte(line))
				if err != nil {
					r.malformed.Add(1)
					continue
				}
				r.deliver(env)
				if env.Ev.Kind() == "hello" && !sawHello && r.change("up", "", false) {
					sawHello = true
					close(hello)
				}
			}
			if split.PendingBytes() > r.maxLineBytes {
				split.Reset()
				r.malformed.Add(1)
				discarding = true
			}
		}
		if err != nil {
			return
		}
	}
}

// readStderr reads f to its end and returns its last non-empty line, trimmed
// and cut to maxDetail UTF-16 units. An incomplete line keeps only its last
// maxStderrPending UTF-16 units.
func readStderr(f *os.File) string {
	var last string
	take := func(b []byte) {
		if s := jsTrim(strings.ToValidUTF8(string(b), "\uFFFD")); s != "" {
			last = text.SliceUTF16(s, maxDetail)
		}
	}
	var pending []byte
	buf := make([]byte, readSize)
	for {
		n, err := f.Read(buf)
		pending = append(pending, buf[:n]...)
		if i := bytes.LastIndexByte(pending, '\n'); i >= 0 {
			for line := range bytes.SplitSeq(pending[:i], []byte{'\n'}) {
				take(line)
			}
			pending = slices.Clone(pending[i+1:])
		}
		pending = tailUTF16(pending, maxStderrPending)
		if err != nil {
			break
		}
	}
	take(pending)
	return last
}

// tailUTF16 returns the end of b that holds at most n UTF-16 units. A rune
// is never cut; each invalid byte counts as one unit.
func tailUTF16(b []byte, n int) []byte {
	// A rune never has more UTF-16 units than bytes, so a short b fits.
	if len(b) <= n {
		return b
	}
	units, i := 0, len(b)
	for i > 0 {
		r, size := utf8.DecodeLastRune(b[:i])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			break
		}
		units += w
		i -= size
	}
	return slices.Clone(b[i:])
}

// jsTrim is String.prototype.trim: it drops white space and line terminators
// at both ends.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(c rune) bool {
		return c == '\uFEFF' || (c != '\u0085' && unicode.IsSpace(c))
	})
}

// deliver calls each subscriber with env.
func (r *Remote) deliver(env wire.Envelope) {
	r.mu.Lock()
	subs := slices.Clone(r.subs)
	r.mu.Unlock()
	for _, s := range subs {
		if !s.removed.Load() {
			safely(func() { s.fn(env) })
		}
	}
}

// change sets the status and, when the state or the detail changed, tells
// the listeners. After Stop it does nothing and returns false, unless force
// is true.
func (r *Remote) change(state, detail string, force bool) bool {
	r.mu.Lock()
	if r.stopped && !force {
		r.mu.Unlock()
		return false
	}
	if r.state == state && r.detail == detail {
		r.mu.Unlock()
		return true
	}
	r.state, r.detail = state, detail
	ls := slices.Clone(r.listeners)
	r.mu.Unlock()
	for _, l := range ls {
		safely(l.fn)
	}
	return true
}

// safely calls fn and recovers a panic: one failing callback must not break
// the others.
func safely(fn func()) {
	defer func() { _ = recover() }()
	fn()
}
