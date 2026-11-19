//go:build darwin || linux

package feed

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/schema"
)

var (
	fakeOnce sync.Once
	fakeBin  string
	fakeErr  error
	fakeDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

// fakeCollector builds the fake collector once and returns its path.
func fakeCollector(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		fakeDir, fakeErr = os.MkdirTemp("", "fakecollector")
		if fakeErr != nil {
			return
		}
		fakeBin = filepath.Join(fakeDir, "fakecollector")
		out, err := exec.Command("go", "build", "-o", fakeBin, "./testdata/fakecollector").CombinedOutput()
		if err != nil {
			fakeErr = fmt.Errorf("build fake collector: %v\n%s", err, out)
		}
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakeBin
}

type event struct {
	at  time.Time
	msg tea.Msg
}

// recorder collects every message that Send receives.
type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) send(m tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event{time.Now(), m})
}

func (r *recorder) snapshot() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.events...)
}

// waitFor polls until pred is true for the recorded events.
func (r *recorder) waitFor(t *testing.T, what string, pred func([]event) bool) []event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := r.snapshot()
		if pred(ev) {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s; got %d events: %v", what, len(ev), summarize(ev))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func summarize(ev []event) []string {
	var s []string
	for _, e := range ev {
		switch m := e.msg.(type) {
		case EnvelopesMsg:
			s = append(s, fmt.Sprintf("envelopes(%d)", len(m)))
		default:
			s = append(s, fmt.Sprintf("%#v", m))
		}
	}
	return s
}

// start runs the feed in a goroutine. The returned stop function cancels it
// and waits for Run to return.
func start(t *testing.T, c Config, r *recorder) (stop func()) {
	t.Helper()
	c.Send = r.send
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, c)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func writeLines(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `{"v":1,"host":"h1","seq":%d,"ts":"2026-01-01T00:00:00.000Z","kind":"agent.status","agentId":"a","status":"working"}`+"\n", i)
	}
	p := filepath.Join(t.TempDir(), "lines.ndjson")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func envelopes(ev []event) []schema.Envelope {
	var out []schema.Envelope
	for _, e := range ev {
		if m, ok := e.msg.(EnvelopesMsg); ok {
			out = append(out, m...)
		}
	}
	return out
}

func states(ev []event) []StateMsg {
	var out []StateMsg
	for _, e := range ev {
		if m, ok := e.msg.(StateMsg); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestRunBatchesEnvelopes(t *testing.T) {
	bin := fakeCollector(t)
	file := writeLines(t, 300)
	var r recorder
	start(t, Config{
		Argv:  []string{bin, "--delay", "1ms", file},
		Batch: 10 * time.Millisecond,
	}, &r)

	ev := r.waitFor(t, "300 envelopes", func(ev []event) bool { return len(envelopes(ev)) >= 300 })
	envs := envelopes(ev)
	if len(envs) != 300 {
		t.Fatalf("got %d envelopes, want 300", len(envs))
	}
	for i, e := range envs {
		if e.Seq != int64(i+1) {
			t.Fatalf("envelope %d has seq %d: order lost", i, e.Seq)
		}
	}
	batches := 0
	for _, e := range ev {
		if _, ok := e.msg.(EnvelopesMsg); ok {
			batches++
		}
	}
	if batches < 3 {
		t.Errorf("got %d batches, want several", batches)
	}
	// "up" comes once, with the host of the first envelope, before the data.
	st := states(ev)
	if len(st) != 1 || st[0] != (StateMsg{Host: "h1", State: "up"}) {
		t.Errorf("states = %#v, want one up for h1", st)
	}
	if _, ok := ev[0].msg.(StateMsg); !ok {
		t.Errorf("first message is %T, want StateMsg", ev[0].msg)
	}
}

func TestRunBigLine(t *testing.T) {
	bin := fakeCollector(t)
	var r recorder
	start(t, Config{Argv: []string{bin, "--big"}, Batch: 10 * time.Millisecond}, &r)

	ev := r.waitFor(t, "big envelope", func(ev []event) bool { return len(envelopes(ev)) >= 1 })
	envs := envelopes(ev)
	if len(envs) != 1 || envs[0].Kind != "snapshot" || envs[0].Host != "big" {
		t.Fatalf("envelopes = %d, first kind %q", len(envs), envs[0].Kind)
	}
	if n := len(envs[0].SnapshotFiles()); n < 1000 {
		t.Errorf("snapshot has %d files, want the whole 200 KiB line", n)
	}
}

func TestRunRestartsWithBackoff(t *testing.T) {
	bin := fakeCollector(t)
	file := writeLines(t, 10)
	var r recorder
	minBackoff := 150 * time.Millisecond
	start(t, Config{
		Argv:       []string{bin, "--exit-after", "5", file},
		Batch:      10 * time.Millisecond,
		MinBackoff: minBackoff,
		MaxBackoff: time.Second,
	}, &r)

	ev := r.waitFor(t, "down then up again", func(ev []event) bool {
		st := states(ev)
		return len(st) >= 3 && st[2].State == "up"
	})
	var seq []string
	var downAt, upAgainAt time.Time
	nStates := 0
	for _, e := range ev {
		m, ok := e.msg.(StateMsg)
		if !ok {
			continue
		}
		nStates++
		seq = append(seq, m.State)
		if nStates == 2 {
			downAt = e.at
			if m.Host != "h1" || m.Detail != "exit status 3" {
				t.Errorf("down = %#v, want host h1 and detail %q", m, "exit status 3")
			}
		}
		if nStates == 3 {
			upAgainAt = e.at
		}
	}
	if strings.Join(seq[:3], ",") != "up,down,up" {
		t.Errorf("states = %v, want up,down,up", seq)
	}
	if gap := upAgainAt.Sub(downAt); gap < minBackoff {
		t.Errorf("restart after %v, want at least %v", gap, minBackoff)
	}
	// The five lines of the first run reach the TUI before the down message.
	var before int
	seenDown := false
	for _, e := range ev {
		if m, ok := e.msg.(StateMsg); ok && m.State == "down" {
			seenDown = true
		}
		if m, ok := e.msg.(EnvelopesMsg); ok && !seenDown {
			before += len(m)
		}
	}
	if before != 5 {
		t.Errorf("%d envelopes before the first down, want 5 (flush on exit)", before)
	}
}

func TestRunMissingBinary(t *testing.T) {
	var r recorder
	start(t, Config{
		Argv:       []string{"/no/such/bin"},
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 100 * time.Millisecond,
	}, &r)

	ev := r.waitFor(t, "two down states", func(ev []event) bool { return len(states(ev)) >= 2 })
	for _, s := range states(ev) {
		if s.State != "down" || s.Host != "" || !strings.Contains(s.Detail, "no such file") {
			t.Errorf("state = %#v, want down with a no-such-file detail", s)
		}
	}
}

func TestRunStderrToDiag(t *testing.T) {
	bin := fakeCollector(t)
	file := writeLines(t, 2)
	var r recorder
	start(t, Config{
		Argv:       []string{bin, "--stderr", "boom", "--exit-after", "1", file},
		Batch:      10 * time.Millisecond,
		MinBackoff: time.Minute,
	}, &r)

	ev := r.waitFor(t, "down", func(ev []event) bool {
		for _, s := range states(ev) {
			if s.State == "down" {
				return true
			}
		}
		return false
	})
	found := false
	for _, e := range ev {
		if d, ok := e.msg.(DiagMsg); ok && d == "boom" {
			found = true
		}
	}
	if !found {
		t.Errorf("no DiagMsg boom in %v", summarize(ev))
	}
	for _, s := range states(ev) {
		if s.State == "down" && s.Detail != "exit status 3 — boom" {
			t.Errorf("down detail = %q, want %q", s.Detail, "exit status 3 — boom")
		}
	}
}

func TestRunSkipsMalformedLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 250; i++ {
		b.WriteString("not json\n")
	}
	b.WriteString("\n")
	b.WriteString(`{"v":1,"host":"h1","seq":1,"kind":"hello"}` + "\n")
	file := filepath.Join(t.TempDir(), "bad.ndjson")
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var r recorder
	start(t, Config{Argv: []string{fakeCollector(t), file}, Batch: 10 * time.Millisecond}, &r)

	ev := r.waitFor(t, "good envelope and two diags", func(ev []event) bool {
		n := 0
		for _, e := range ev {
			if _, ok := e.msg.(DiagMsg); ok {
				n++
			}
		}
		return len(envelopes(ev)) == 1 && n >= 2
	})
	var diags []string
	for _, e := range ev {
		if d, ok := e.msg.(DiagMsg); ok {
			diags = append(diags, string(d))
		}
	}
	want := []string{"skipped 100 malformed lines", "skipped 200 malformed lines"}
	if strings.Join(diags, "|") != strings.Join(want, "|") {
		t.Errorf("diags = %q, want %q", diags, want)
	}
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d still exists after %v", pid, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startedPid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not write its pid file")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunStopsChildOnCancel(t *testing.T) {
	bin := fakeCollector(t)
	file := writeLines(t, 1)
	pidFile := filepath.Join(t.TempDir(), "pid")
	var r recorder
	stop := start(t, Config{
		Argv:  []string{bin, "--pidfile", pidFile, file},
		Batch: 10 * time.Millisecond,
	}, &r)

	pid := startedPid(t, pidFile)
	r.waitFor(t, "up", func(ev []event) bool { return len(states(ev)) >= 1 })
	begin := time.Now()
	stop()
	waitGone(t, pid, 3*time.Second)
	if d := time.Since(begin); d > 3*time.Second {
		t.Errorf("stop took %v", d)
	}
	for _, s := range states(r.snapshot()) {
		if s.State == "down" {
			t.Errorf("unexpected down state after a deliberate stop: %#v", s)
		}
	}
}

func TestRunKillsChildThatIgnoresTerm(t *testing.T) {
	bin := fakeCollector(t)
	file := writeLines(t, 1)
	pidFile := filepath.Join(t.TempDir(), "pid")
	var r recorder
	stop := start(t, Config{
		Argv:      []string{bin, "--ignore-term", "--pidfile", pidFile, file},
		Batch:     10 * time.Millisecond,
		stopGrace: 200 * time.Millisecond,
	}, &r)

	pid := startedPid(t, pidFile)
	r.waitFor(t, "up", func(ev []event) bool { return len(states(ev)) >= 1 })
	stop()
	waitGone(t, pid, time.Second)
}

func TestRunReturnsWhenCancelledDuringBackoff(t *testing.T) {
	var r recorder
	stop := start(t, Config{
		Argv:       []string{"/no/such/bin"},
		MinBackoff: time.Hour,
		MaxBackoff: time.Hour,
	}, &r)
	r.waitFor(t, "down", func(ev []event) bool { return len(states(ev)) >= 1 })
	begin := time.Now()
	stop()
	if d := time.Since(begin); d > time.Second {
		t.Errorf("Run took %v to notice cancel during backoff", d)
	}
}

func TestNextBackoff(t *testing.T) {
	const min, max, reset = time.Second, 30 * time.Second, 60 * time.Second
	cur := min
	var waits []time.Duration
	for i := 0; i < 7; i++ {
		var w time.Duration
		w, cur = nextBackoff(cur, min, max, 0, reset)
		waits = append(waits, w)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i, w := range waits {
		if w != want[i]*time.Second {
			t.Fatalf("waits = %v, want 1,2,4,8,16,30,30 s", waits)
		}
	}
	// A child that stayed up long enough resets the delay.
	w, next := nextBackoff(16*time.Second, min, max, 60*time.Second, reset)
	if w != min || next != 2*time.Second {
		t.Errorf("after a long run: wait %v next %v, want 1s and 2s", w, next)
	}
	w, _ = nextBackoff(16*time.Second, min, max, 59*time.Second, reset)
	if w != 16*time.Second {
		t.Errorf("after a short run: wait %v, want 16s", w)
	}
}

func TestDefaultArgv(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		t.Setenv("AGENT_VIEW_COLLECTOR", `["/bin/echo","hi"]`)
		got, err := DefaultArgv([]string{"--demo"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, " ") != "/bin/echo hi" {
			t.Errorf("argv = %q, want the override without flags", got)
		}
	})
	for name, val := range map[string]string{
		"bad JSON":     `not json`,
		"not strings":  `[1,2]`,
		"empty array":  `[]`,
		"empty string": `["a",""]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AGENT_VIEW_COLLECTOR", val)
			if _, err := DefaultArgv(nil); err == nil {
				t.Error("want an error")
			}
		})
	}
	t.Run("default with cli.ts", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "cli.ts"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		t.Setenv("AGENT_VIEW_COLLECTOR", "")
		got, err := DefaultArgv([]string{"--demo", "--linger", "5"})
		if err != nil {
			t.Fatal(err)
		}
		want := "bun src/cli.ts collect --stdio --demo --linger 5"
		if strings.Join(got, " ") != want {
			t.Errorf("argv = %q, want %q", got, want)
		}
	})
	t.Run("not in the repository", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("AGENT_VIEW_COLLECTOR", "")
		_, err := DefaultArgv(nil)
		want := "run from the agent-view repository root, or set AGENT_VIEW_COLLECTOR"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	})
}
