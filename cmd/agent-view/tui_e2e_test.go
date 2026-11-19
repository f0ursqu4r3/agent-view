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

// tuiOpts configures startTUI. A zero home gets a new temporary directory.
type tuiOpts struct {
	home string
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
	name := regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(t.Name(), "-")
	p := &tuiPane{t: t, sock: fmt.Sprintf("av-%s-%d", name, os.Getpid())}

	env := []string{"HOME=" + o.home}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "HOME", k == "TMUX", k == "TMUX_PANE", k == "CLAUDE_CONFIG_DIR", k == "CODEX_HOME", k == "NO_COLOR",
			strings.HasPrefix(k, "AGENT_VIEW_"):
			continue
		}
		env = append(env, kv)
	}
	argv := []string{"-L", p.sock, "-f", "/dev/null", "new-session", "-d", "-s", "main",
		"-x", "100", "-y", "30", "-c", t.TempDir(), "--", binPath}
	argv = append(argv, o.args...)
	argv = append(argv, ";", "set-option", "-t", "main", "remain-on-exit", "on")
	cmd := exec.Command("tmux", argv...)
	cmd.Env = append(env, o.env...)
	// kill-server leaves the socket file, so the file goes after the server.
	t.Cleanup(func() {
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

// history returns the pane with its scrollback.
func (p *tuiPane) history() string { return p.tmux("capture-pane", "-p", "-S", "-", "-t", "main") }

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
	n, err := strconv.Atoi(p.format("#{pane_dead_status}"))
	if err != nil {
		p.t.Fatalf("pane_dead_status: %v", err)
	}
	return n
}

// newMarker returns a sleep argument unique to this test run. pgrep -f on it
// finds only the processes of this test.
func newMarker(t *testing.T) string {
	t.Helper()
	m := fmt.Sprintf("600.%d%06d", os.Getpid(), rand.IntN(1_000_000))
	// SIGKILL only the PIDs found by the marker, if the test fails before the
	// program stops them.
	t.Cleanup(func() {
		out, _ := exec.Command("pgrep", "-f", "sleep "+m).Output()
		for _, pid := range strings.Fields(string(out)) {
			_ = exec.Command("kill", "-KILL", pid).Run()
		}
	})
	return m
}

// waitNoMarked waits up to 1 s until no process holds the marker.
func waitNoMarked(t *testing.T, marker string) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); pgrepCount(t, "sleep "+marker) != 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d marked processes remain", pgrepCount(t, "sleep "+marker))
		}
	}
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

// startDemoRemote starts --demo --remote fakehost with a remote that prints
// the well-formed stream and then sleeps with marker; prefix runs first.
func startDemoRemote(t *testing.T, prefix, marker string) *tuiPane {
	t.Helper()
	stream := remoteStream(t)
	script := prefix + "cat '" + stream + "'; exec sleep " + marker
	p := startTUI(t, tuiOpts{env: []string{remoteEnv(t, script)}, args: []string{"--demo", "--remote", "fakehost"}})
	p.waitScreen("both hosts up and the remote agent", func(s string) bool {
		return hostUp(s, localLabel()) && hostUp(s, "fakehost") && strings.Contains(s, "remote-agent-e2e")
	})
	for deadline := time.Now().Add(paneWait); pgrepCount(t, "sleep "+marker) != 1; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d marked processes, want 1", pgrepCount(t, "sleep "+marker))
		}
	}
	return p
}

// --demo with one remote: both hosts come up with the remote's agent; q exits
// 0 and leaves no remote child.
func TestTUIDemoAndRemote(t *testing.T) {
	marker := newMarker(t)
	p := startDemoRemote(t, "", marker)
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
	}
	waitNoMarked(t, marker)
}

// writeClaudeLog writes one Claude session log under home, with records and a
// file time one hour old: a custom title and two finished tool calls.
func writeClaudeLog(t *testing.T, home, title string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-e2e-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	ts := func(s int) string {
		return base.Add(time.Duration(s) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	rec := func(s int, typ, rest string) string {
		return fmt.Sprintf(`{"sessionId":"e2e-session","cwd":"/e2e/proj","timestamp":%q,"type":%q%s}`, ts(s), typ, rest)
	}
	lines := []string{
		rec(0, "user", `,"message":{"content":"start"}`),
		rec(1, "custom-title", `,"customTitle":"`+title+`"`),
		rec(2, "assistant", `,"message":{"model":"m-1","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.txt"}}]}`),
		rec(3, "user", `,"message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}`),
		rec(4, "assistant", `,"message":{"model":"m-1","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"echo x"}}]}`),
		rec(5, "user", `,"message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"x"}]}`),
	}
	path := filepath.Join(dir, "e2e-session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, base, base); err != nil {
		t.Fatal(err)
	}
}

// An agent from a log written an hour ago is history: its row and the
// "loaded" line show, and no BOOT line does.
func TestTUIStartupHistory(t *testing.T) {
	home := t.TempDir()
	writeClaudeLog(t, home, "startup-history-agent")
	// --linger 180 keeps the hour-old agent on the board (default: 10 min).
	p := startTUI(t, tuiOpts{home: home, args: []string{"--linger", "180"}})
	loaded := regexp.MustCompile(`loaded \d+ agent`)
	s := p.waitScreen("the history agent and the loaded line", func(s string) bool {
		return strings.Contains(s, "startup-history-agent") && loaded.MatchString(s)
	})
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
			marker := newMarker(t)
			p := startDemoRemote(t, "", marker)
			if out, err := exec.Command("kill", "-"+sig, strconv.Itoa(p.pid())).CombinedOutput(); err != nil {
				t.Fatalf("kill -%s: %v: %s", sig, err, out)
			}
			if code := p.waitExit(); code != 0 {
				t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
			}
			waitNoMarked(t, marker)
		})
	}
}

// A remote child that ignores SIGTERM does not hold the exit: q exits 0
// within 3 s and the child is gone.
func TestTUIQuitWithStubbornRemote(t *testing.T) {
	marker := newMarker(t)
	p := startDemoRemote(t, "trap '' TERM; ", marker)
	p.keys("q")
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit status %d, want 0:\n%s", code, p.history())
	}
	waitNoMarked(t, marker)
}

// A remote that sends garbage, one hello and half a line, then exits 1: its
// host shows down and connects again; the local host stays up; the program
// runs until q, and the coverage panel holds the reason.
func TestTUIBrokenRemote(t *testing.T) {
	hello := strings.TrimSuffix(string(wire.EncodeLine(wire.Envelope{V: 1, Host: "fakehost", Seq: 0, Ts: "2026-01-01T00:00:00.000Z",
		Ev: wire.Hello{CollectorVersion: "0", Platform: "t", Now: "2026-01-01T00:00:00.000Z"}})), "\n")
	script := `printf 'garbage {{{\n'; printf '%s\n' '` + hello + `'; printf '{"v":1,"kind":"agent.upsert",'; exit 1`
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
		st := statusLine(s)
		return strings.Contains(st, "fakehost ● connecting") || strings.Contains(st, "fakehost ● up")
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
}

// What a remote writes on stderr never reaches the board: it shows in the
// coverage panel, and once on the real stderr after the program exits.
func TestTUIQuietTerminal(t *testing.T) {
	marker := newMarker(t)
	flag := filepath.Join(t.TempDir(), "ran")
	// The first run fails like ssh (exit 255) with the marker on stderr; later
	// runs wait, so the detail is held once.
	script := "if [ -e '" + flag + "' ]; then exec sleep " + marker + "; fi; touch '" + flag +
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
	waitNoMarked(t, marker)
	out := p.history()
	if n := strings.Count(out, mark); n != 1 {
		t.Fatalf("the marker shows %d times after exit, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "fakehost collector down: ssh failed: "+mark) {
		t.Fatalf("held diagnostics missing after exit:\n%s", out)
	}
}
