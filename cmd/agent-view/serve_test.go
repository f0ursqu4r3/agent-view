package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

type hostStatus struct {
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type snapshotBody struct {
	Hosts     []hostStatus     `json:"hosts"`
	Envelopes []map[string]any `json:"envelopes"`
}

var agentViewURL = regexp.MustCompile(`^Agent View: (http://127\.0\.0\.1:\d+)\n$`)

// startServe starts "agent-view serve --port 0" with extra arguments and
// returns the process and the URL it prints.
func startServe(t *testing.T, env []string, extra ...string) (*collector, string) {
	t.Helper()
	c := startWithEnv(t, env, append([]string{"serve", "--port", "0"}, extra...)...)
	line := c.readLine(t, 5*time.Second)
	m := agentViewURL.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("first line = %q, want Agent View: <url>", line)
	}
	return c, m[1]
}

// getSnapshot reads /api/snapshot.
func getSnapshot(t *testing.T, url string) snapshotBody {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body snapshotBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

// pollSnapshot polls /api/snapshot every 100 ms for up to 5 s until ok.
func pollSnapshot(t *testing.T, url string, ok func(snapshotBody) bool) snapshotBody {
	t.Helper()
	var body snapshotBody
	for i := 0; i < 50; i++ {
		body = getSnapshot(t, url)
		if ok(body) {
			return body
		}
		time.Sleep(100 * time.Millisecond)
	}
	return body
}

// expectedLabel is the host name up to its first dot, or "local".
func expectedLabel(t *testing.T) string {
	t.Helper()
	h, _ := os.Hostname()
	label, _, _ := strings.Cut(h, ".")
	if label == "" {
		label = "local"
	}
	return label
}

// hub mode prints the URL, serves /api/snapshot with the local host up, and exits 0 on SIGTERM
func TestServePrintsURLAndSnapshot(t *testing.T) {
	c, url := startServe(t, nil, "--demo")
	label := expectedLabel(t)
	body := pollSnapshot(t, url, func(b snapshotBody) bool {
		return len(b.Hosts) > 0 && b.Hosts[0].State == "up"
	})
	if len(body.Hosts) != 1 || body.Hosts[0] != (hostStatus{Label: label, Kind: "local", State: "up"}) {
		t.Fatalf("hosts = %+v, want one local host %q up", body.Hosts, label)
	}
	if len(body.Envelopes) < 2 {
		t.Fatalf("envelopes = %d, want at least 2", len(body.Envelopes))
	}
	if e := body.Envelopes[0]; e["kind"] != "hello" || e["host"] != label || e["demo"] != true {
		t.Fatalf("envelope 0 = %v", e)
	}
	if e := body.Envelopes[1]; e["kind"] != "snapshot" {
		t.Fatalf("envelope 1 = %v", e)
	}
	c.cmd.Process.Signal(syscall.SIGTERM)
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
}

// hub mode: --remote adds one remote host per flag; an unreachable remote never stops the hub
func TestServeRemoteHosts(t *testing.T) {
	// The test-only override replaces ssh, so the test never runs ssh through the owner's SSH config.
	env := []string{`AGENT_VIEW_REMOTE_COMMAND=["sh","-c","exit 3"]`}
	c, url := startServe(t, env, "--demo", "--remote", "box1.invalid", "--remote", "box1.invalid")
	body := pollSnapshot(t, url, func(b snapshotBody) bool {
		if len(b.Hosts) != 3 || b.Hosts[0].State != "up" {
			return false
		}
		return b.Hosts[1].State == "down" && b.Hosts[2].State == "down"
	})
	want := []hostStatus{
		{Label: expectedLabel(t), Kind: "local", State: "up"},
		{Label: "box1.invalid", Kind: "remote", State: "down", Detail: "collector exited 3"},
		{Label: "box1.invalid#2", Kind: "remote", State: "down", Detail: "collector exited 3"},
	}
	if len(body.Hosts) != len(want) {
		t.Fatalf("hosts = %+v, want %+v", body.Hosts, want)
	}
	for i := range want {
		if body.Hosts[i] != want[i] {
			t.Fatalf("hosts = %+v, want %+v", body.Hosts, want)
		}
	}
	c.cmd.Process.Signal(syscall.SIGINT)
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
	if s := c.stderr.String(); strings.Contains(s, "failed") || strings.Contains(s, "panic") {
		t.Fatalf("stderr = %q", s)
	}
}

// A bad AGENT_VIEW_REMOTE_COMMAND is a usage error: the message alone on
// stderr, exit code 2, and nothing on stdout.
func TestServeBadRemoteCommand(t *testing.T) {
	for _, bad := range []string{"sh", "[]", "[1]", `{"a":1}`, "null"} {
		var out, errb bytes.Buffer
		cmd := exec.Command(binPath, "serve", "--port", "0", "--remote", "box1.invalid")
		cmd.Env = append(os.Environ(), "AGENT_VIEW_REMOTE_COMMAND="+bad)
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Fatalf("%q: err = %v, want exit 2", bad, err)
		}
		const want = "agent-view: AGENT_VIEW_REMOTE_COMMAND must be a JSON array of strings (test only)\n"
		if errb.String() != want || out.Len() != 0 {
			t.Fatalf("%q: stderr %q, stdout %q", bad, errb.String(), out.String())
		}
	}
}

// pgrepCount returns how many processes have pattern in their command line.
func pgrepCount(t *testing.T, pattern string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return 0 // no match
	}
	if err != nil {
		t.Skipf("pgrep unavailable: %v", err)
	}
	return len(strings.Fields(string(out)))
}

// A listener holds the port: serve exits 1 within 3 s with the message, and
// the hub's local collector and remotes are stopped (no child stays).
func TestServePortInUse(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	marker := fmt.Sprintf("sleep 987.%d", time.Now().UnixNano()%1_000_000)
	var out, errb bytes.Buffer
	cmd := exec.Command(binPath, "serve", "--port", fmt.Sprint(port), "--demo", "--remote", "box1.invalid")
	cmd.Env = append(os.Environ(), `AGENT_VIEW_REMOTE_COMMAND=["sh","-c","`+marker+`"]`)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("err = %v, want exit 1 (stderr %q)", err, errb.String())
		}
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		t.Fatal("serve still running 3 s after a port-in-use error")
	}
	prefix := fmt.Sprintf("agent-view: cannot start the server on port %d: ", port)
	if !strings.HasPrefix(errb.String(), prefix) || !strings.Contains(errb.String(), "address already in use") {
		t.Fatalf("stderr = %q, want prefix %q", errb.String(), prefix)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	// A leftover child would show for a moment at least.
	for i := 0; i < 5; i++ {
		if n := pgrepCount(t, marker); n != 0 {
			t.Fatalf("%d child processes left running", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A remote child that ignores SIGTERM does not outlive serve: when its stop
// bound runs out, serve sends SIGKILL to the child's process group and exits
// 0 within 2.5 s, and no marked process is left.
func TestServeKillsStubbornRemote(t *testing.T) {
	marker := fmt.Sprintf("sleep 986.%d", time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", marker).Run() })
	// The shell and its sleep both ignore SIGTERM, and both command lines
	// hold the marker.
	env := []string{`AGENT_VIEW_REMOTE_COMMAND=["sh","-c","trap '' TERM; ` + marker + ` & wait"]`}
	c, _ := startServe(t, env, "--demo", "--remote", "box1.invalid")
	for i := 0; pgrepCount(t, marker) != 2; i++ {
		if i == 50 {
			t.Fatalf("%d marked processes, want 2", pgrepCount(t, marker))
		}
		time.Sleep(100 * time.Millisecond)
	}
	began := time.Now()
	c.cmd.Process.Signal(syscall.SIGTERM)
	if code := c.waitExit(t, 2500*time.Millisecond); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
	if took := time.Since(began); took < 1400*time.Millisecond {
		t.Fatalf("serve exited after %v: the child did not ignore SIGTERM", took)
	}
	for i := 0; ; i++ {
		n := pgrepCount(t, marker)
		if n == 0 {
			break
		}
		if i == 10 {
			t.Fatalf("%d marked processes left 1 s after serve exited", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type auditReport struct {
	Counts map[string]struct {
		Agents int `json:"agents"`
	} `json:"counts"`
}

// runAuditCmd runs "agent-view audit --port <port>".
func runAuditCmd(t *testing.T, port string) (out, errs string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	cmd := exec.Command(binPath, "audit", "--port", port)
	cmd.Stdout, cmd.Stderr = &o, &e
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return o.String(), e.String(), ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return o.String(), e.String(), 0
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatal("audit still running after 15 s")
		return "", "", -1
	}
}

// audit prints the report of a running hub and exits 0; it exits 1 with a
// message when no hub answers.
func TestAuditCommand(t *testing.T) {
	c, url := startServe(t, nil, "--demo")
	port := url[strings.LastIndex(url, ":")+1:]
	var report auditReport
	var out, errs string
	var code int
	for i := 0; i < 50; i++ {
		out, errs, code = runAuditCmd(t, port)
		if code == 0 {
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("audit output is not JSON: %q: %v", out, err)
			}
			if report.Counts["claude"].Agents > 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code != 0 || report.Counts["claude"].Agents <= 0 {
		t.Fatalf("audit: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if !strings.HasSuffix(out, "\n") || !strings.HasPrefix(out, "{\n  \"counts\"") {
		t.Fatalf("audit output = %q, want indented JSON and a newline", out)
	}
	c.cmd.Process.Signal(syscall.SIGTERM)
	c.waitExit(t, 2*time.Second)

	// The hub is gone: its port is closed now.
	out, errs, code = runAuditCmd(t, port)
	if code != 1 || out != "" || !strings.HasPrefix(errs, "agent-view: audit: ") {
		t.Fatalf("audit on a closed port: exit %d, stdout %q, stderr %q", code, out, errs)
	}
}

// audit --port 0 is a usage error: the TypeScript script asserts the port is above 0.
func TestAuditPortZero(t *testing.T) {
	_, errs, code := runAuditCmd(t, "0")
	if code != 2 || !strings.HasPrefix(errs, "agent-view: Invalid port: 0") {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
}
