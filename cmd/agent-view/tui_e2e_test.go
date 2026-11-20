package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-view/internal/wire"
)

// The end-to-end tests run the binary that TestMain builds in a tmux pane on
// a private socket, 100 × 30, with HOME set to a temporary directory.

const (
	paneWait = 15 * time.Second
	exitWait = 3 * time.Second
)

// tuiPane is one running agent-view in its own tmux server.
type tuiPane struct {
	t    *testing.T
	sock string
}

// tuiOpts configures startTUI. A zero home gets a new temporary directory; a
// zero bin runs the binary that TestMain built.
type tuiOpts struct {
	home string
	bin  string
	env  []string // extra KEY=value entries
	args []string
}

// startTUI starts agent-view as the pane command (not through a shell), so
// the pane PID is the program's. The pane stays after the program exits.
func startTUI(t *testing.T, o tuiOpts) *tuiPane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if o.home == "" {
		o.home = t.TempDir()
	}
	if o.bin == "" {
		o.bin = binPath
	}
	name := regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(t.Name(), "-")
	p := &tuiPane{t: t, sock: fmt.Sprintf("av-%s-%d", name, os.Getpid())}

	env := isolatedEnv(o.home)
	argv := []string{"-L", p.sock, "-f", "/dev/null", "new-session", "-d", "-s", "main",
		"-x", "100", "-y", "30", "-c", t.TempDir(), "--", o.bin}
	argv = append(argv, o.args...)
	argv = append(argv, ";", "set-option", "-t", "main", "remain-on-exit", "on")
	cmd := exec.Command("tmux", argv...)
	cmd.Env = append(env, o.env...)
	// kill-server leaves the socket file, so the file goes after the server.
	// kill-server sends SIGHUP, which a program that already has a shutdown
	// request ignores: a program that still runs gets SIGKILL by its PID
	// first, and the cleanup waits until tmux sees it dead, so that it cannot
	// start a remote child again. The stubPIDs cleanup, registered before,
	// then kills the recorded stubs.
	t.Cleanup(func() {
		paneDead := func() string {
			out, _ := exec.Command("tmux", "-L", p.sock, "list-panes", "-t", "main", "-F", "#{pane_dead}").Output()
			return strings.TrimSpace(string(out))
		}
		if paneDead() == "0" {
			out, _ := exec.Command("tmux", "-L", p.sock, "list-panes", "-t", "main", "-F", "#{pane_pid}").Output()
			if pid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				for deadline := time.Now().Add(exitWait); paneDead() == "0" && time.Now().Before(deadline); {
					time.Sleep(20 * time.Millisecond)
				}
			}
		}
		path, _ := exec.Command("tmux", "-L", p.sock, "display-message", "-p", "#{socket_path}").Output()
		_ = exec.Command("tmux", "-L", p.sock, "kill-server").Run()
		if s := strings.TrimSpace(string(path)); s != "" {
			_ = os.Remove(s)
		}
	})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	return p
}

// isolatedEnv returns this process's environment with HOME set to home.
// The collector reads HOME, CLAUDE_CONFIG_DIR and CODEX_HOME: the others are
// dropped (and XDG_*, TMUX*, NO_COLOR, AGENT_VIEW_*), so a test never reads
// the owner's logs.
func isolatedEnv(home string) []string {
	env := []string{"HOME=" + home}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "HOME", k == "TMUX", k == "TMUX_PANE", k == "CLAUDE_CONFIG_DIR", k == "CODEX_HOME", k == "NO_COLOR",
			strings.HasPrefix(k, "AGENT_VIEW_"), strings.HasPrefix(k, "XDG_"):
			continue
		}
		env = append(env, kv)
	}
	return env
}

func (p *tuiPane) tmux(args ...string) string {
	p.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", p.sock}, args...)...).CombinedOutput()
	if err != nil {
		p.t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return string(out)
}

// screen returns the visible pane.
func (p *tuiPane) screen() string { return p.tmux("capture-pane", "-p", "-t", "main") }

// screenJoined is screen with each wrapped line joined to the next, so a text
// that wraps at the pane edge stays in one piece.
func (p *tuiPane) screenJoined() string { return p.tmux("capture-pane", "-p", "-J", "-t", "main") }

// history returns the pane with its scrollback.
func (p *tuiPane) history() string { return p.tmux("capture-pane", "-p", "-S", "-", "-t", "main") }

// historyJoined is history with each wrapped line joined to one line, so a
// text longer than the pane width stays in one piece.
func (p *tuiPane) historyJoined() string {
	return p.tmux("capture-pane", "-p", "-J", "-S", "-", "-t", "main")
}

func (p *tuiPane) format(f string) string {
	return strings.TrimSpace(p.tmux("list-panes", "-t", "main", "-F", f))
}

func (p *tuiPane) pid() int {
	n, err := strconv.Atoi(p.format("#{pane_pid}"))
	if err != nil {
		p.t.Fatal(err)
	}
	return n
}

func (p *tuiPane) keys(k ...string) { p.tmux(append([]string{"send-keys", "-t", "main"}, k...)...) }

// waitScreen polls the pane every 100 ms until ok, for up to paneWait.
func (p *tuiPane) waitScreen(what string, ok func(string) bool) string {
	p.t.Helper()
	var s string
	for deadline := time.Now().Add(paneWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if p.dead() {
			p.t.Fatalf("waiting for %s: the program exited (status %s):\n%s", what, p.format("#{pane_dead_status}"), p.history())
		}
		if s = p.screen(); ok(s) {
			return s
		}
	}
	p.t.Fatalf("waiting for %s: timed out; pane:\n%s", what, s)
	return ""
}

func (p *tuiPane) dead() bool { return p.format("#{pane_dead}") == "1" }

// waitExit waits up to exitWait for the program to exit and returns its exit
// status. The alternate screen must be off: the terminal is restored.
func (p *tuiPane) waitExit() int {
	p.t.Helper()
	began := time.Now()
	for !p.dead() {
		if time.Since(began) > exitWait {
			p.t.Fatalf("the program did not exit within %v; pane:\n%s", exitWait, p.screen())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if alt := p.format("#{alternate_on}"); alt != "0" {
		p.t.Fatalf("alternate screen still on after exit")
	}
	status := p.format("#{pane_dead_status}")
	if sig := p.format("#{pane_dead_signal}"); status == "" && sig != "" {
		p.t.Fatalf("the program was killed by signal %s:\n%s", sig, p.history())
	}
	n, err := strconv.Atoi(status)
	if err != nil {
		p.t.Fatalf("pane_dead_status: %v", err)
	}
	return n
}

// remoteEnv returns AGENT_VIEW_REMOTE_COMMAND for a remote that runs script.
func remoteEnv(t *testing.T, script string) string {
	t.Helper()
	b, err := json.Marshal([]string{"sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	return "AGENT_VIEW_REMOTE_COMMAND=" + string(b)
}

// remoteStream writes a well-formed remote stream to a file: hello, a
// snapshot with the agent remote-agent-e2e and one open call.
func remoteStream(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	const id = "claude:e2e:remote-agent-e2e"
	cwd := "/e2e"
	envs := []wire.Envelope{
		{V: 1, Host: "fakehost", Seq: 0, Ts: now, Ev: wire.Hello{CollectorVersion: "0", Platform: "t", Now: now}},
		{V: 1, Host: "fakehost", Seq: 0, Ts: now, Ev: wire.Snapshot{
			Agents: []wire.Agent{{
				ID: id, Provider: "claude", Name: "remote-agent-e2e", Role: "main", Link: "root",
				Cwd: &cwd, ProjectRoot: &cwd, Status: "working", LastEventAt: now,
			}},
			OpenCalls: []wire.Call{{
				AgentID: id, CallID: "c1", StartedAt: now, Category: "command", RawName: "Bash", Command: "echo e2e",
			}},
		}},
	}
	var b []byte
	for _, e := range envs {
		b = append(b, wire.EncodeLine(e)...)
	}
	path := filepath.Join(t.TempDir(), "stream.ndjson")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// statusLine returns the first line of the pane: the status bar.
func statusLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// hostUp reports whether the status bar shows label as up.
func hostUp(s, label string) bool { return strings.Contains(statusLine(s), label+" ● up") }

// startDemoRemote starts --demo --remote fakehost with a remote that records
// its PID in pids, prints the well-formed stream and then sleeps; prefix runs
// first. It returns when the one remote process runs.
func startDemoRemote(t *testing.T, prefix string, pids *stubPIDs) *tuiPane {
	t.Helper()
	return startDemoRemoteWith(t, tuiOpts{}, prefix, pids)
}

// startDemoRemoteWith is startDemoRemote with the home and the binary of o.
func startDemoRemoteWith(t *testing.T, o tuiOpts, prefix string, pids *stubPIDs) *tuiPane {
	t.Helper()
	stream := remoteStream(t)
	script := prefix + pids.record("$$") + "cat '" + stream + "'; exec sleep 600"
	o.env = append(o.env, remoteEnv(t, script))
	o.args = append(o.args, "--demo", "--remote", "fakehost")
	p := startTUI(t, o)
	p.waitScreen("both hosts up", func(s string) bool {
		return hostUp(s, localLabel()) && hostUp(s, "fakehost")
	})
	// At 100 × 30 the demo's lanes can push the remote lane off the board.
	// Hosts keep their first-appearance order, so the remote lane is the first
	// or the last: selecting the last row (G), then the first (g), in turn,
	// scrolls the board until the remote agent shows.
	keys := []string{"G", "g"}
	n := 0
	p.waitScreen("the remote agent", func(s string) bool {
		if strings.Contains(s, "remote-agent-e2e") {
			return true
		}
		if n%5 == 4 { // every 500 ms
			k := keys[(n/5)%2]
			t.Logf("the remote lane is off the board: pressing %s", k)
			p.keys(k)
		}
		n++
		return false
	})
	pids.waitRunning(1, paneWait)
	return p
}

// --demo with one remote: both hosts come up with the remote's agent; q exits
// 0 and leaves no remote child.
func TestTUIDemoAndRemote(t *testing.T) {
	pids := newStubPIDs(t)
	p := startDemoRemote(t, "", pids)
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
	}
	pids.waitGone(time.Second, "remain")
}

// writeClaudeLog writes one Claude session under home, with records and
// file times one hour old: the main log has a custom title and two finished
// tool calls; a sub-agent log has one finished tool call.
func writeClaudeLog(t *testing.T, home, title string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-e2e-proj")
	subDir := filepath.Join(dir, "e2e-session", "subagents")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	ts := func(s int) string {
		return base.Add(time.Duration(s) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	rec := func(s int, typ, rest string) string {
		return fmt.Sprintf(`{"sessionId":"e2e-session","cwd":"/e2e/proj","timestamp":%q,"type":%q%s}`, ts(s), typ, rest)
	}
	write := func(path string, lines ...string) {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, base, base); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "e2e-session.jsonl"),
		rec(0, "user", `,"message":{"content":"start"}`),
		rec(1, "custom-title", `,"customTitle":"`+title+`"`),
		rec(2, "assistant", `,"message":{"model":"m-1","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.txt"}}]}`),
		rec(3, "user", `,"message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}`),
		rec(4, "assistant", `,"message":{"model":"m-1","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"echo x"}}]}`),
		rec(5, "user", `,"message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"x"}]}`),
	)
	write(filepath.Join(subDir, "agent-sub1.jsonl"),
		rec(10, "user", `,"isSidechain":true,"message":{"content":"x"}`),
		rec(11, "assistant", `,"isSidechain":true,"message":{"model":"m-1","content":[{"type":"tool_use","id":"u1","name":"Read","input":{"file_path":"b.txt"}}]}`),
		rec(12, "user", `,"isSidechain":true,"message":{"content":[{"type":"tool_result","tool_use_id":"u1","content":"x"}]}`),
	)
}

// Agents from logs written an hour ago are history: the main agent's row and
// the "loaded" line show, with every finished call counted, and the
// sub-agent gets no BOOT line. The bridge subscribes before the scan, so the
// calls arrive as events; a late subscriber gets a snapshot without the
// finished calls and the count is 0.
func TestTUIStartupHistory(t *testing.T) {
	home := t.TempDir()
	writeClaudeLog(t, home, "startup-history-agent")
	// --linger 180 keeps the hour-old agents on the board (default: 10 min).
	p := startTUI(t, tuiOpts{home: home, args: []string{"--linger", "180"}})
	loaded := regexp.MustCompile(`loaded \d+ agents?, \d+ calls? from logs`)
	s := p.waitScreen("the history agent and the loaded line", func(s string) bool {
		return strings.Contains(s, "startup-history-agent") && loaded.MatchString(s)
	})
	if got := loaded.FindString(s); got != "loaded 2 agents, 3 calls from logs" {
		t.Fatalf("summary = %q, want %q:\n%s", got, "loaded 2 agents, 3 calls from logs", s)
	}
	if strings.Contains(s, "BOOT") {
		t.Fatalf("a BOOT line for a history agent:\n%s", s)
	}
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0", code)
	}
}

// SIGTERM and SIGHUP, sent to the program's own PID, end it within 3 s with
// the terminal restored and no remote child left.
func TestTUISignals(t *testing.T) {
	for _, sig := range []string{"TERM", "HUP"} {
		t.Run(sig, func(t *testing.T) {
			pids := newStubPIDs(t)
			p := startDemoRemote(t, "", pids)
			if out, err := exec.Command("kill", "-"+sig, strconv.Itoa(p.pid())).CombinedOutput(); err != nil {
				t.Fatalf("kill -%s: %v: %s", sig, err, out)
			}
			if code := p.waitExit(); code != 0 {
				t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
			}
			pids.waitGone(time.Second, "remain")
		})
	}
}

// A remote child that ignores SIGTERM does not hold the exit: q exits 0
// within 3 s and the child is gone.
func TestTUIQuitWithStubbornRemote(t *testing.T) {
	pids := newStubPIDs(t)
	p := startDemoRemote(t, "trap '' TERM; ", pids)
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
	}
	pids.waitGone(time.Second, "remain")
}

// A remote that sends garbage, one hello and half a line, then exits 1: its
// host shows down and then connecting again; the local host stays up; the program
// runs until q, and the coverage panel holds the reason.
func TestTUIBrokenRemote(t *testing.T) {
	hello := strings.TrimSuffix(string(wire.EncodeLine(wire.Envelope{V: 1, Host: "fakehost", Seq: 0, Ts: "2026-01-01T00:00:00.000Z",
		Ev: wire.Hello{CollectorVersion: "0", Platform: "t", Now: "2026-01-01T00:00:00.000Z"}})), "\n")
	pids := newStubPIDs(t)
	flag := filepath.Join(t.TempDir(), "ran")
	// Later runs wait without a hello, so the host stays connecting: a host
	// that cycles through connecting and up in milliseconds is hard to see.
	script := "if [ -e '" + flag + "' ]; then " + pids.record("$$") + "exec sleep 600; fi; touch '" + flag + "'; " +
		`printf 'garbage {{{\n'; printf '%s\n' '` + hello + `'; printf '{"v":1,"kind":"agent.upsert",'; exit 1`
	p := startTUI(t, tuiOpts{env: []string{remoteEnv(t, script)}, args: []string{"--demo", "--remote", "fakehost"}})
	p.waitScreen("the remote down", func(s string) bool {
		if strings.Contains(statusLine(s), "fakehost ● down") {
			if !hostUp(s, localLabel()) {
				t.Fatalf("local host not up:\n%s", s)
			}
			return true
		}
		return false
	})
	p.waitScreen("the remote connecting again", func(s string) bool {
		return strings.Contains(statusLine(s), "fakehost ● connecting")
	})
	if s := p.screen(); !hostUp(s, localLabel()) {
		t.Fatalf("local host not up:\n%s", s)
	}
	p.keys("c")
	p.waitScreen("the down reason in the coverage panel", func(s string) bool {
		return strings.Contains(s, "fakehost collector down: collector exited 1")
	})
	if p.dead() {
		t.Fatal("the program exited before q")
	}
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0", code)
	}
	pids.waitGone(time.Second, "remain")
}

// What a remote writes on stderr never reaches the board: it shows in the
// coverage panel, and once on the real stderr after the program exits.
func TestTUIQuietTerminal(t *testing.T) {
	pids := newStubPIDs(t)
	flag := filepath.Join(t.TempDir(), "ran")
	// The first run fails like ssh (exit 255) with the marker on stderr; later
	// runs wait, so the detail is held once.
	script := "if [ -e '" + flag + "' ]; then " + pids.record("$$") + "exec sleep 600; fi; touch '" + flag +
		"'; echo QUIET-TEST-MARKER >&2; exit 255"
	p := startTUI(t, tuiOpts{env: []string{remoteEnv(t, script)}, args: []string{"--demo", "--remote", "fakehost"}})
	const mark = "QUIET-TEST-MARKER"
	noMark := func(s string) {
		if strings.Contains(s, mark) {
			t.Fatalf("the board shows the marker:\n%s", s)
		}
	}
	p.waitScreen("the remote down", func(s string) bool {
		noMark(s)
		return strings.Contains(statusLine(s), "fakehost ● down")
	})
	// Watch the board for a while longer: the remote starts again.
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		noMark(p.screen())
	}
	p.keys("c")
	p.waitScreen("the marker in the coverage panel", func(s string) bool {
		return strings.Contains(s, "fakehost collector down: ssh failed: "+mark)
	})
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0", code)
	}
	pids.waitGone(time.Second, "remain")
	out := p.history()
	if n := strings.Count(out, mark); n != 1 {
		t.Fatalf("the marker shows %d times after exit, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "fakehost collector down: ssh failed: "+mark) {
		t.Fatalf("held diagnostics missing after exit:\n%s", out)
	}
}

// buildAvtest builds the program with the avtest tag (see avtest_hook.go) and
// returns the path of the binary.
func buildAvtest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-view-avtest")
	if out, err := exec.Command("go", "build", "-tags", "avtest", "-o", path, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build -tags avtest: %v\n%s", err, out)
	}
	return path
}

// waitFile polls every 20 ms until path exists, for up to paneWait.
func waitFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(paneWait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

// The program itself writes to os.Stderr and calls log.Printf while the TUI
// runs. Neither text may show on the board. After q each shows once on the
// real terminal. This pins the stderr capture and log.SetOutput in runTUI;
// TestTUIQuietTerminal sends its text through a remote child, which never
// touches fd 2 of the program.
//
// The log package writes to fd 2 by default, so the capture alone also takes a
// short log line. The capture cuts a line to 4,096 bytes and log.SetOutput
// does not: the log line is longer than that, and its tail arrives only
// through log.SetOutput. The test binary has the avtest tag, which adds a
// goroutine that writes on request (avtest_hook.go).
func TestTUIProcessOwnWrites(t *testing.T) {
	home := t.TempDir()
	hook := filepath.Join(home, "avtest-hook")
	if err := os.MkdirAll(hook, 0o700); err != nil {
		t.Fatal(err)
	}
	p := startTUI(t, tuiOpts{home: home, bin: buildAvtest(t)})
	p.waitScreen("the board", func(s string) bool { return hostUp(s, localLabel()) })

	nonce := fmt.Sprintf("%d-%06d", os.Getpid(), rand.IntN(1_000_000))
	stderrMark, logHead, logTail := "AVTEST-STDERR-"+nonce, "AVTEST-LOGHEAD-"+nonce, "AVTEST-LOGTAIL-"+nonce
	marks := []string{stderrMark, logHead, logTail}
	// A text written at the pane edge wraps, so the joined screen is read.
	noMarks := func() {
		s := p.screenJoined()
		for _, m := range marks {
			if strings.Contains(s, m) {
				t.Fatalf("the board shows %s:\n%s", m, s)
			}
		}
	}
	// The hook reads the request only when the file is complete: write, then rename.
	ask := filepath.Join(hook, "go")
	if err := os.WriteFile(ask+".tmp", []byte(strings.Join(marks, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ask+".tmp", ask); err != nil {
		t.Fatal(err)
	}

	// The program has written when done exists. The board is watched all the
	// time from the request, and for a while after the writes.
	done := filepath.Join(hook, "done")
	for deadline := time.Now().Add(paneWait); ; time.Sleep(20 * time.Millisecond) {
		noMarks()
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the program did not write within %v; pane:\n%s", paneWait, p.screen())
		}
	}
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		noMarks()
	}
	// Both texts reached the diagnostics: the coverage panel shows them.
	p.keys("c")
	p.waitScreen("the texts in the coverage panel", func(s string) bool {
		return strings.Contains(s, stderrMark) && strings.Contains(s, logHead)
	})
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
	}
	out := p.historyJoined()
	for _, m := range marks {
		if n := strings.Count(out, m); n != 1 {
			t.Fatalf("%s shows %d times after exit, want 1:\n%s", m, n, out)
		}
	}
}

// signalNames maps the subtest names of the signal tests to the signals that
// kill(1) and the avtest hook take.
var signalNames = []string{"TERM", "INT", "HUP"}

// startWithSignalHook starts the avtest binary with --demo and one remote
// stub that records its PID. The hook file <HOME>/avtest-hook/<file> holds
// sig: the program sends sig to itself at that point (avtest_hook.go).
func startWithSignalHook(t *testing.T, bin, file, sig string, pids *stubPIDs) *tuiPane {
	t.Helper()
	home := t.TempDir()
	hook := filepath.Join(home, "avtest-hook")
	if err := os.MkdirAll(hook, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hook, file), []byte(sig+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := pids.record("$$") + "exec sleep 600"
	return startTUI(t, tuiOpts{home: home, bin: bin, env: []string{remoteEnv(t, script)},
		args: []string{"--demo", "--remote", "fakehost"}})
}

// A signal that arrives before the sources start ends the program with exit
// status 0 and the terminal restored; the remote stub never starts.
func TestTUISignalBeforeStartExitsCleanly(t *testing.T) {
	bin := buildAvtest(t)
	for _, sig := range signalNames {
		t.Run(sig, func(t *testing.T) {
			pids := newStubPIDs(t)
			p := startWithSignalHook(t, bin, "signal-before-start", sig, pids)
			if code := p.waitExit(); code != 0 {
				t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
			}
			if got := pids.recorded(); len(got) != 0 {
				t.Fatalf("the remote stub started (PIDs %v), want none", got)
			}
		})
	}
}

// A signal that arrives after the sources start and before the program runs
// ends the program with exit status 0; the remote stub is gone.
func TestTUISignalBeforeRunExitsCleanly(t *testing.T) {
	bin := buildAvtest(t)
	for _, sig := range signalNames {
		t.Run(sig, func(t *testing.T) {
			pids := newStubPIDs(t)
			p := startWithSignalHook(t, bin, "signal-before-run", sig, pids)
			if code := p.waitExit(); code != 0 {
				t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
			}
			t.Logf("%d stub PIDs recorded", len(pids.recorded()))
			pids.waitGone(time.Second, "remain")
		})
	}
}

// A second and a third SIGTERM while the program shuts down (a remote that
// ignores SIGTERM holds the shutdown for about 1.5 s) do not kill it: it
// exits 0, prints the held diagnostics once and leaves no stub.
func TestTUIRepeatedSignalsDuringShutdownKeepDiagnostics(t *testing.T) {
	home := t.TempDir()
	hook := filepath.Join(home, "avtest-hook")
	if err := os.MkdirAll(hook, 0o700); err != nil {
		t.Fatal(err)
	}
	pids := newStubPIDs(t)
	p := startDemoRemoteWith(t, tuiOpts{home: home, bin: buildAvtest(t)}, "trap '' TERM; ", pids)

	nonce := fmt.Sprintf("%d-%06d", os.Getpid(), rand.IntN(1_000_000))
	marks := []string{"AVTEST-STDERR-" + nonce, "AVTEST-LOGHEAD-" + nonce, "AVTEST-LOGTAIL-" + nonce}
	ask := filepath.Join(hook, "go")
	if err := os.WriteFile(ask+".tmp", []byte(strings.Join(marks, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ask+".tmp", ask); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(hook, "done"))

	p.keys("q")
	// The alternate screen goes off when the program has stopped and the
	// shutdown has begun.
	for deadline := time.Now().Add(exitWait); p.format("#{alternate_on}") != "0"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the program did not leave the alternate screen within %v", exitWait)
		}
	}
	pid := strconv.Itoa(p.pid())
	for i := range 2 {
		if i > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		// A kill that finds no process leaves the cause to waitExit.
		if out, err := exec.Command("kill", "-TERM", pid).CombinedOutput(); err != nil {
			t.Logf("SIGTERM %d: %v: %s", i+1, err, out)
		}
	}
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
	}
	pids.waitGone(time.Second, "remain")
	out := p.historyJoined()
	for _, m := range marks {
		if n := strings.Count(out, m); n != 1 {
			t.Fatalf("%s shows %d times after exit, want 1:\n%s", m, n, out)
		}
	}
}
