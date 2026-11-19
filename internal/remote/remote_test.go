//go:build darwin || linux

package remote

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"agent-view/internal/hub"
	"agent-view/internal/wire"
)

var _ hub.Source = (*Remote)(nil)

// escapeEnv makes the test binary a helper process: it leaves its process
// group, writes its pid to the file the variable names, and sleeps while it
// holds the stdout and stderr it inherited.
const escapeEnv = "AGENT_VIEW_REMOTE_TEST_ESCAPE"

func TestMain(m *testing.M) {
	if pidFile := os.Getenv(escapeEnv); pidFile != "" {
		_ = syscall.Setpgid(0, 0)
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// line encodes env without its newline.
func line(env wire.Envelope) string {
	return strings.TrimSuffix(string(wire.EncodeLine(env)), "\n")
}

const ts = "2026-01-01T00:00:00.000Z"

func helloLine(host string) string {
	return line(wire.Envelope{V: 1, Host: host, Seq: 0, Ts: ts, Ev: wire.Hello{CollectorVersion: "0", Platform: "t", Now: ts}})
}

func snapshotLine(host string) string {
	return line(wire.Envelope{V: 1, Host: host, Seq: 0, Ts: ts, Ev: wire.Snapshot{}})
}

// upsertLine is a valid agent.upsert.
func upsertLine(host string, seq int64) string {
	return line(wire.Envelope{V: 1, Host: host, Seq: seq, Ts: ts, Ev: wire.AgentUpsert{Agent: wire.Agent{
		ID: "a", Provider: "claude", Name: "a", Role: "main", Link: "root", Status: "idle", LastEventAt: ts,
	}}})
}

// printLines is a shell command that prints each line with its newline.
func printLines(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("printf '%s\\n' " + shellQuote(l) + "; ")
	}
	return b.String()
}

// demoScript replaces TypeScript's demo collector: it appends its pid to
// pidFile, prints a hello and a snapshot, and stays up.
func demoScript(pidFile string) []string {
	return []string{"sh", "-c", "echo $$ >> " + shellQuote(pidFile) + "; " +
		printLines(helloLine("devbox"), snapshotLine("devbox")) + "exec sleep 30"}
}

// spawns records each call of the command function.
type spawns struct {
	mu    sync.Mutex
	times []time.Time
}

// command returns a command function that records the call and returns
// argv(n), where n counts the calls from 0.
func (s *spawns) command(argv func(n int) []string) func(string, string) []string {
	return func(string, string) []string {
		s.mu.Lock()
		n := len(s.times)
		s.times = append(s.times, time.Now())
		s.mu.Unlock()
		return argv(n)
	}
}

func (s *spawns) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.times)
}

func (s *spawns) all() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.times)
}

func fixed(argv ...string) func(int) []string { return func(int) []string { return argv } }

// newRemote fills in the test defaults (host devbox, backoff [50, 400]) and
// stops the remote when the test ends.
func newRemote(t *testing.T, o Options) *Remote {
	t.Helper()
	if o.SSHHost == "" {
		o.SSHHost = "devbox"
	}
	if o.Label == "" {
		o.Label = "devbox"
	}
	if o.BackoffMs == [2]int64{} {
		o.BackoffMs = [2]int64{50, 400}
	}
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	return r
}

// watcher records envelopes and status changes, as "state" or
// "state:detail".
type watcher struct {
	mu     sync.Mutex
	envs   []wire.Envelope
	states []string
}

func watch(r *Remote) *watcher {
	w := &watcher{}
	r.Subscribe(func(e wire.Envelope) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.envs = append(w.envs, e)
	})
	r.OnStatusChange(func() {
		s := r.Status()
		w.mu.Lock()
		defer w.mu.Unlock()
		if s.Detail != "" {
			w.states = append(w.states, s.State+":"+s.Detail)
		} else {
			w.states = append(w.states, s.State)
		}
	})
	return w
}

func (w *watcher) got() []wire.Envelope {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.envs)
}

func (w *watcher) seen() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.states)
}

func count(envs []wire.Envelope, kind string) int {
	n := 0
	for _, e := range envs {
		if e.Ev.Kind() == kind {
			n++
		}
	}
	return n
}

func kindSeqs(envs []wire.Envelope) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Ev.Kind() + ":" + strconv.FormatInt(e.Seq, 10)
	}
	return out
}

// until waits for cond, at most within.
func until(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(within)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const wait = 15 * time.Second

// pids reads the pids that the children wrote to file, in order.
func pids(t *testing.T, file string) []int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, f := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pid)
	}
	return out
}

// waitGone waits until no process has the pid.
func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	until(t, within, "process "+strconv.Itoa(pid)+" to end", func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	})
}

func start(t *testing.T, r *Remote) {
	t.Helper()
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
}

// remote source parses NDJSON from the child and reports up after hello
func TestRemoteParsesAndReportsUp(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed(demoScript(pidFile)...))})
	w := watch(r)
	if got, want := r.Status(), (wire.HostStatus{Label: "devbox", Kind: "remote", State: "connecting"}); got != want {
		t.Errorf("status = %+v, want %+v", got, want)
	}
	start(t, r)
	until(t, wait, "up and a snapshot", func() bool {
		return r.Status().State == "up" && count(w.got(), "snapshot") > 0
	})
	got := w.got()
	if e := got[0]; e.V != 1 || e.Ev.Kind() != "hello" || e.Host != "devbox" {
		t.Errorf("first envelope = %+v, want a v1 hello from devbox", e)
	}
	if s := w.seen(); !slices.Equal(s, []string{"up"}) {
		t.Errorf("states = %q, want [up]", s)
	}
	if k := r.Kind(); k != "remote" {
		t.Errorf("kind = %q, want remote", k)
	}
}

// child exit triggers reconnect with backoff and a fresh hello + snapshot
func TestRemoteReconnectsAfterExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed(demoScript(pidFile)...))})
	w := watch(r)
	start(t, r)
	until(t, wait, "up", func() bool { return r.Status().State == "up" })
	if err := syscall.Kill(pids(t, pidFile)[0], syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	until(t, 20*time.Second, "a second up and snapshot", func() bool {
		ups := 0
		for _, s := range w.seen() {
			if s == "up" {
				ups++
			}
		}
		return ups >= 2 && count(w.got(), "snapshot") >= 2
	})
	want := []string{"up", "down:collector exited SIGTERM", "connecting", "up"}
	if s := w.seen(); !slices.Equal(s[:min(len(s), 4)], want) {
		t.Errorf("states = %q, want %q", s, want)
	}
	got := w.got()
	if n := count(got, "hello"); n != 2 {
		t.Errorf("%d hellos, want 2", n)
	}
	if n := count(got, "snapshot"); n < 2 {
		t.Errorf("%d snapshots, want at least 2", n)
	}
	if n := sp.count(); n != 2 {
		t.Errorf("%d spawns, want 2", n)
	}
}

// Review Focus 5: exit 127 sets down with "agent-view not found on devbox"
// and retries with growing delay
func TestRemoteExit127RetriesWithGrowingDelay(t *testing.T) {
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed("sh", "-c", "exit 127"))})
	w := watch(r)
	start(t, r)
	until(t, wait, "4 spawns", func() bool { return sp.count() >= 4 })
	if !slices.Contains(w.seen(), "down:agent-view not found on devbox") {
		t.Errorf("states = %q, want down:agent-view not found on devbox", w.seen())
	}
	if s := r.Status().State; s != "down" && s != "connecting" {
		t.Errorf("state = %q, want down or connecting", s)
	}
	times := sp.all()
	var gaps []time.Duration
	for i := 1; i < len(times); i++ {
		gaps = append(gaps, times[i].Sub(times[i-1]))
	}
	if gaps[0] < 45*time.Millisecond {
		t.Errorf("first gap %v, want at least 45ms", gaps[0])
	}
	if gaps[1] <= gaps[0] || gaps[2] <= gaps[1] {
		t.Errorf("gaps %v do not grow", gaps[:3])
	}
}

// exit 255 reports the last stderr line; other codes report the code
func TestRemoteExitDetails(t *testing.T) {
	ssh := newRemote(t, Options{
		Command: func(string, string) []string {
			return []string{"sh", "-c", `echo first >&2; echo "  Permission denied (publickey).  " >&2; exit 255`}
		},
		BackoffMs: [2]int64{5000, 5000},
	})
	start(t, ssh)
	until(t, wait, "down", func() bool { return ssh.Status().State == "down" })
	if d := ssh.Status().Detail; d != "ssh failed: Permission denied (publickey)." {
		t.Errorf("detail = %q", d)
	}

	other := newRemote(t, Options{
		Command:   func(string, string) []string { return []string{"sh", "-c", "exit 3"} },
		BackoffMs: [2]int64{5000, 5000},
	})
	start(t, other)
	until(t, wait, "down", func() bool { return other.Status().State == "down" })
	if d := other.Status().Detail; d != "collector exited 3" {
		t.Errorf("detail = %q", d)
	}
}

// malformed child lines are skipped and counted, stream continues
//
// TypeScript delivers an agent.upsert without an agent; the Go decoder
// rejects it, so it is the sixth malformed line, and a valid agent.upsert
// shows that the stream continues.
func TestRemoteSkipsMalformedLines(t *testing.T) {
	hello := `{"v":1,"kind":"hello","host":"x","seq":0,"ts":"2026-01-01T00:00:00.000Z","collectorVersion":"0","platform":"t","now":"2026-01-01T00:00:00.000Z"}`
	lines := []string{
		"not json", "[1,2]", `{"v":2,"kind":"hello","host":"x","seq":0,"ts":"t"}`, `{"v":1,"kind":"hello"}`,
		`{"v":1,"kind":"hello","host":"x","seq":"1","ts":"t"}`, hello,
		`{"v":1,"kind":"agent.upsert","host":"x","seq":1,"ts":"2026-01-01T00:00:01.000Z"}`,
		upsertLine("x", 2),
	}
	r := newRemote(t, Options{Command: func(string, string) []string {
		return []string{"sh", "-c", printLines(lines...) + "exec sleep 30"}
	}})
	w := watch(r)
	start(t, r)
	until(t, wait, "2 envelopes", func() bool { return len(w.got()) >= 2 })
	if got := kindSeqs(w.got()); !slices.Equal(got, []string{"hello:0", "agent.upsert:2"}) {
		t.Errorf("envelopes = %q, want [hello:0 agent.upsert:2]", got)
	}
	if s := r.Status().State; s != "up" {
		t.Errorf("state = %q, want up", s)
	}
	if n := r.MalformedLines(); n != 6 {
		t.Errorf("malformed = %d, want 6", n)
	}
}

// stop resolves after the child exits and never respawns
func TestRemoteStopEndsChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed(demoScript(pidFile)...))})
	start(t, r)
	until(t, wait, "up", func() bool { return r.Status().State == "up" })
	pid := pids(t, pidFile)[0]
	r.Stop()
	// Stop has reaped the child, so the pid is free at once.
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("child %d still exists after Stop (kill: %v)", pid, err)
	}
	if s := r.Status().State; s != "down" {
		t.Errorf("state = %q, want down", s)
	}
	n := sp.count()
	time.Sleep(300 * time.Millisecond)
	if got := sp.count(); got != n {
		t.Errorf("%d spawns after Stop, want %d", got, n)
	}
}

// stop during backoff cancels the retry timer
func TestRemoteStopDuringBackoff(t *testing.T) {
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed("sh", "-c", "exit 1")), BackoffMs: [2]int64{200, 200}})
	start(t, r)
	until(t, wait, "down", func() bool { return r.Status().State == "down" })
	r.Stop()
	n := sp.count()
	time.Sleep(400 * time.Millisecond)
	if got := sp.count(); got != n {
		t.Errorf("%d spawns after Stop, want %d", got, n)
	}
}

// a spawn that throws sets down and retries
//
// Go has no spawn option: a program that does not exist makes the spawn fail.
func TestRemoteSpawnFailure(t *testing.T) {
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed(filepath.Join(t.TempDir(), "no-such-program")))})
	start(t, r)
	until(t, wait, "a second spawn and down", func() bool { return sp.count() >= 2 && r.Status().State == "down" })
	d := r.Status().Detail
	if !strings.HasPrefix(d, "spawn failed: ") || !strings.Contains(d, "no such file or directory") {
		t.Errorf("detail = %q, want spawn failed: ... no such file or directory", d)
	}
}

// an oversize line without a newline is dropped and counted once; the stream
// continues
//
// The big line is a valid envelope, so only the size rule keeps it out.
func TestRemoteOversizeLine(t *testing.T) {
	hello := `{"v":1,"kind":"hello","host":"x","seq":0,"ts":"t","collectorVersion":"0","platform":"t","now":"t"}`
	script := strings.Join([]string{
		printLines(hello),
		`printf '{"v":1,"kind":"agent.upsert","host":"x","seq":1,"ts":"t","agent":{"id":"big"},"pad":"'; head -c 300000 /dev/zero | tr '\0' x; printf '"}\n'; `,
		printLines(upsertLine("x", 2)),
		"exec sleep 30",
	}, "")
	r := newRemote(t, Options{Command: func(string, string) []string { return []string{"sh", "-c", script} }, MaxLineBytes: 1024})
	w := watch(r)
	start(t, r)
	until(t, wait, "2 envelopes", func() bool { return len(w.got()) >= 2 })
	if got := kindSeqs(w.got()); !slices.Equal(got, []string{"hello:0", "agent.upsert:2"}) {
		t.Errorf("envelopes = %q, want [hello:0 agent.upsert:2]", got)
	}
	if n := r.MalformedLines(); n != 1 {
		t.Errorf("malformed = %d, want 1", n)
	}
}

// constructor rejects ssh host values that could inject options or shell
// text
func TestNewRejectsBadHosts(t *testing.T) {
	for _, host := range []string{"-oProxyCommand=x", "host;id", "a b", "$(x)", "`id`", "", "a\nb"} {
		r, err := New(Options{SSHHost: host, Label: "x"})
		if err == nil {
			r.Stop()
			t.Errorf("New(%q) returned no error", host)
		} else if err.Error() != "invalid ssh host: use a host name or user@host" {
			t.Errorf("New(%q) error = %q", host, err)
		}
	}
	for _, host := range []string{"user@host.example", "_h-1.example:22"} {
		if _, err := New(Options{SSHHost: host, Label: host}); err != nil {
			t.Errorf("New(%q): %v", host, err)
		}
	}
}

// default command passes the label single-quoted for the remote shell
func TestDefaultCommand(t *testing.T) {
	cmd := DefaultCommand("user@host.example", "user@host.example")
	if want := []string{"ssh", "-T", "-o", "BatchMode=yes", "-o", "ServerAliveInterval=15", "user@host.example"}; !slices.Equal(cmd[:7], want) {
		t.Errorf("cmd[:7] = %q, want %q", cmd[:7], want)
	}
	if want := []string{"agent-view", "collect", "--stdio", "--label", "'user@host.example'"}; !slices.Equal(cmd[7:12], want) {
		t.Errorf("cmd[7:12] = %q, want %q", cmd[7:12], want)
	}
	if last := cmd[len(cmd)-1]; last != "'user@host.example'" {
		t.Errorf("last = %q", last)
	}
	cmd = DefaultCommand("h", "a'b;id")
	if last := cmd[len(cmd)-1]; last != `'a'\''b;id'` {
		t.Errorf("last = %q, want %q", last, `'a'\''b;id'`)
	}
}

// Review Focus 2: a child that ignores SIGTERM, with a grandchild in its
// group, gets SIGKILL 2 s after SIGTERM; Stop returns within 3 s and leaves
// no process of the group.
func TestRemoteKillsStubbornChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	script := "trap '' TERM; sleep 30 & echo $$ $! > " + shellQuote(pidFile) + "; " +
		printLines(helloLine("devbox")) + "wait"
	r := newRemote(t, Options{Command: func(string, string) []string { return []string{"sh", "-c", script} }})
	start(t, r)
	until(t, wait, "up", func() bool { return r.Status().State == "up" })
	ps := pids(t, pidFile)
	if len(ps) != 2 {
		t.Fatalf("pids = %v, want the child and the grandchild", ps)
	}
	began := time.Now()
	r.Stop()
	took := time.Since(began)
	if took >= 3*time.Second {
		t.Errorf("Stop took %v, want under 3s", took)
	}
	if took < 1500*time.Millisecond {
		t.Errorf("Stop took %v: the child did not ignore SIGTERM", took)
	}
	for _, pid := range ps {
		waitGone(t, pid, 2*time.Second)
	}
}

// Review Focus 3: a connection that drops mid-line never delivers the partial
// line; the host goes down with a detail and a retry follows.
func TestRemoteMidLineDrop(t *testing.T) {
	var sp spawns
	r := newRemote(t, Options{
		Command: sp.command(func(n int) []string {
			if n == 0 {
				return []string{"sh", "-c", printLines(helloLine("devbox")) +
					`printf '%s' '{"v":1,"kind":"agent.upsert","host":"devbox","seq":1,'; exit 1`}
			}
			return []string{"sh", "-c", "exec sleep 30"}
		}),
		BackoffMs: [2]int64{100, 100},
	})
	w := watch(r)
	start(t, r)
	until(t, wait, "a retry", func() bool { return sp.count() >= 2 })
	if got := kindSeqs(w.got()); !slices.Equal(got, []string{"hello:0"}) {
		t.Errorf("envelopes = %q, want [hello:0]", got)
	}
	want := []string{"up", "down:collector exited 1", "connecting"}
	until(t, wait, "connecting", func() bool { return len(w.seen()) >= 3 })
	if s := w.seen(); !slices.Equal(s, want) {
		t.Errorf("states = %q, want %q", s, want)
	}
	if n := r.MalformedLines(); n != 0 {
		t.Errorf("malformed = %d, want 0", n)
	}
}

// Review Focus 4: a valid envelope with a field of the wrong type, or without
// the object its kind needs, is skipped and counted; the stream continues.
func TestRemoteBadFieldTypes(t *testing.T) {
	lines := []string{
		helloLine("x"),
		`{"v":1,"kind":"agent.upsert","host":"x","seq":"7","ts":"t","agent":{"id":"a"}}`,
		`{"v":1,"kind":"tool.start","host":"x","seq":2,"ts":"t","call":"x"}`,
		`{"v":1,"kind":"tool.start","host":"x","seq":3,"ts":"t","call":null}`,
		`{"v":1,"kind":"coverage","host":"x","seq":4,"ts":"t"}`,
		upsertLine("x", 5),
	}
	r := newRemote(t, Options{Command: func(string, string) []string {
		return []string{"sh", "-c", printLines(lines...) + "exec sleep 30"}
	}})
	w := watch(r)
	start(t, r)
	until(t, wait, "2 envelopes", func() bool { return len(w.got()) >= 2 })
	if got := kindSeqs(w.got()); !slices.Equal(got, []string{"hello:0", "agent.upsert:5"}) {
		t.Errorf("envelopes = %q, want [hello:0 agent.upsert:5]", got)
	}
	if n := r.MalformedLines(); n != 4 {
		t.Errorf("malformed = %d, want 4", n)
	}
}

// A child that stays up for the stable time after its hello starts the retry
// delay over.
func TestRemoteStableResetsBackoff(t *testing.T) {
	var sp spawns
	r := newRemote(t, Options{
		Command: sp.command(func(n int) []string {
			switch {
			case n < 3:
				return []string{"sh", "-c", "exit 1"}
			case n == 3:
				return []string{"sh", "-c", printLines(helloLine("devbox")) + "sleep 0.5; exit 1"}
			default:
				return []string{"sh", "-c", "exec sleep 30"}
			}
		}),
		BackoffMs: [2]int64{50, 5000},
	})
	r.stableUp = 200 * time.Millisecond
	start(t, r)
	until(t, wait, "5 spawns", func() bool { return sp.count() >= 5 })
	times := sp.all()
	// The waits are 50, 100 and 200 ms, then the child is up for 500 ms.
	// Without the reset the next wait is 400 ms; with it, 50 ms.
	if gap := times[4].Sub(times[3]); gap >= 850*time.Millisecond {
		t.Errorf("gap after the stable child = %v, want about 550ms", gap)
	}
}

// A process outside the child's group that holds its stdout and stderr does
// not keep the remote from seeing the exit, nor Stop from returning.
func TestRemoteEscapedPipeHolder(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "holder")
	script := escapeEnv + "=" + shellQuote(pidFile) + " " + shellQuote(exe) + " & " +
		"while [ ! -s " + shellQuote(pidFile) + " ]; do sleep 0.05; done; " +
		printLines(helloLine("devbox")) + "exit 1"
	r := newRemote(t, Options{
		Command:   func(string, string) []string { return []string{"sh", "-c", script} },
		BackoffMs: [2]int64{5000, 5000},
	})
	w := watch(r)
	start(t, r)
	until(t, wait, "the holder", func() bool {
		data, err := os.ReadFile(pidFile)
		return err == nil && len(data) > 0
	})
	holder := pids(t, pidFile)[0]
	t.Cleanup(func() { _ = syscall.Kill(holder, syscall.SIGKILL) })
	until(t, wait, "down", func() bool { return slices.Contains(w.seen(), "down:collector exited 1") })
	began := time.Now()
	r.Stop()
	if took := time.Since(began); took >= time.Second {
		t.Errorf("Stop took %v", took)
	}
	if err := syscall.Kill(holder, 0); err != nil {
		t.Errorf("the holder is gone (%v): the test did not hold the pipes", err)
	}
}

// The hub merges a remote's stream: a new subscriber gets its hello and
// snapshot, Hosts reports it up, and the hub's Stop ends the child.
func TestRemoteInHub(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	r := newRemote(t, Options{Label: "box", Command: func(string, string) []string { return demoScript(pidFile) }})
	h := hub.New(hub.Options{})
	t.Cleanup(h.Stop)
	if err := h.AddSource(r); err != nil {
		t.Fatal(err)
	}
	start(t, r)
	until(t, wait, "the hub's replay", func() bool { return len(h.SnapshotAll()) == 2 })
	var got []string
	for _, e := range h.SnapshotAll() {
		got = append(got, e.Host+":"+e.Ev.Kind())
	}
	if want := []string{"box:hello", "box:snapshot"}; !slices.Equal(got, want) {
		t.Errorf("replay = %q, want %q", got, want)
	}
	if hosts := h.Hosts(); len(hosts) != 1 || hosts[0] != (wire.HostStatus{Label: "box", Kind: "remote", State: "up"}) {
		t.Errorf("hosts = %+v", hosts)
	}
	pid := pids(t, pidFile)[0]
	h.Stop()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("child %d still exists after the hub's Stop (kill: %v)", pid, err)
	}
	if s := r.Status().State; s != "down" {
		t.Errorf("state = %q, want down", s)
	}
}

// sent records the signals a Remote sends to its child's process group, and
// sends them.
type sent struct {
	mu   sync.Mutex
	sigs []string // "pid:SIGNAME"
}

func (s *sent) signal(pid int, sig syscall.Signal) {
	s.mu.Lock()
	s.sigs = append(s.sigs, strconv.Itoa(pid)+":"+unixSignalName(sig))
	s.mu.Unlock()
	killGroup(pid, sig)
}

func (s *sent) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sigs)
}

func unixSignalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	}
	return strconv.Itoa(int(sig))
}

// Kill sends SIGKILL to the group of a child that ignores SIGTERM at once: a
// Stop in flight returns long before its own SIGKILL, no process of the group
// is left, and no child starts again.
func TestRemoteKillEndsStubbornChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	script := "trap '' TERM; sleep 30 & echo $$ $! > " + shellQuote(pidFile) + "; " +
		printLines(helloLine("devbox")) + "wait"
	var sp spawns
	r := newRemote(t, Options{Command: sp.command(fixed("sh", "-c", script))})
	start(t, r)
	until(t, wait, "up", func() bool { return r.Status().State == "up" })
	ps := pids(t, pidFile)
	if len(ps) != 2 {
		t.Fatalf("pids = %v, want the child and the grandchild", ps)
	}
	began := time.Now()
	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()
	time.Sleep(300 * time.Millisecond)
	r.Kill()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if took := time.Since(began); took >= 1500*time.Millisecond {
		t.Errorf("Stop took %v after Kill, want well under the 2s SIGKILL", took)
	}
	for _, pid := range ps {
		waitGone(t, pid, time.Second)
	}
	if s := r.Status().State; s != "down" {
		t.Errorf("state = %q, want down", s)
	}
	time.Sleep(300 * time.Millisecond)
	if n := sp.count(); n != 1 {
		t.Errorf("%d spawns, want 1", n)
	}
}

// Kill with no child sends no signal: not before Start (and Start then starts
// nothing), and not to a child that has exited and been reaped. Kill cancels
// the retry, so no child starts after it.
func TestRemoteKillWithoutChild(t *testing.T) {
	var sp spawns
	var sig sent
	idle := newRemote(t, Options{Command: sp.command(fixed("sh", "-c", "exec sleep 30"))})
	idle.signal = sig.signal
	idle.Kill()
	start(t, idle)
	time.Sleep(200 * time.Millisecond)
	if n := sp.count(); n != 0 {
		t.Errorf("%d spawns after Kill and Start, want 0", n)
	}
	if s := sig.all(); len(s) != 0 {
		t.Errorf("signals = %q, want none", s)
	}

	pidFile := filepath.Join(t.TempDir(), "pids")
	var sp2 spawns
	var sig2 sent
	r := newRemote(t, Options{
		Command:   sp2.command(fixed("sh", "-c", "echo $$ >> "+shellQuote(pidFile)+"; exit 0")),
		BackoffMs: [2]int64{300, 300},
	})
	r.signal = sig2.signal
	w := watch(r)
	start(t, r)
	// down comes after run has returned: the child is reaped.
	until(t, wait, "down", func() bool { return slices.Contains(w.seen(), "down:collector exited 0") })
	first := pids(t, pidFile)[0]
	before := len(sig2.all())
	r.Kill()
	n := sp2.count()
	for _, s := range sig2.all()[before:] {
		if s == strconv.Itoa(first)+":SIGKILL" {
			t.Errorf("Kill sent %s to the reaped child", s)
		}
	}
	time.Sleep(600 * time.Millisecond)
	if got := sp2.count(); got != n {
		t.Errorf("%d spawns after Kill, want %d", got, n)
	}
}

// Kill and Stop called together, while children start and exit quickly,
// neither panic nor hang, and every signal reaches a child that was not yet
// reaped.
func TestRemoteKillRacesStopAndExit(t *testing.T) {
	var sig sent
	for i := 0; i < 20; i++ {
		r := newRemote(t, Options{
			Command:   func(string, string) []string { return []string{"sh", "-c", "exit 0"} },
			BackoffMs: [2]int64{1, 2},
		})
		r.signal = func(pid int, s syscall.Signal) {
			// signal runs under the Remote's lock, so child is stable here.
			if r.child != pid {
				t.Errorf("signal %d to %d, but the current child is %d", s, pid, r.child)
			}
			// A zombie still has its pid; a reaped child does not.
			if err := syscall.Kill(pid, 0); err != nil {
				t.Errorf("signal %d to %d after its reap: %v", s, pid, err)
			}
			sig.signal(pid, s)
		}
		start(t, r)
		time.Sleep(time.Duration(i%7) * time.Millisecond)
		done := make(chan struct{}, 2)
		go func() { r.Kill(); done <- struct{}{} }()
		go func() { r.Stop(); done <- struct{}{} }()
		for j := 0; j < 2; j++ {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("round %d: Kill or Stop did not return", i)
			}
		}
		r.Kill() // after Stop: no child, nothing to do
	}
	if len(sig.all()) == 0 {
		t.Fatal("no signal was sent: the test checked nothing")
	}
}
