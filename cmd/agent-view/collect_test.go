package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-view/internal/cli"
)

// binPath is the agent-view binary that TestMain builds once.
var binPath string

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "agent-view-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binPath = filepath.Join(dir, "agent-view")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// collector is a running binary with its stdout pipe.
type collector struct {
	cmd    *exec.Cmd
	stdout *os.File // the read end
	lines  *bufio.Reader
	stderr bytes.Buffer
	exited chan error
}

// startCollector runs the binary with argv and a pipe as stdout.
func startCollector(t *testing.T, argv ...string) *collector {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{stdout: r, lines: bufio.NewReader(r), exited: make(chan error, 1)}
	c.cmd = exec.Command(binPath, argv...)
	c.cmd.Stdout = w
	c.cmd.Stderr = &c.stderr
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close() // the child holds the write end now
	go func() { c.exited <- c.cmd.Wait() }()
	t.Cleanup(func() {
		c.cmd.Process.Kill()
		r.Close()
	})
	return c
}

// readLine reads one line within d.
func (c *collector) readLine(t *testing.T, d time.Duration) string {
	t.Helper()
	c.stdout.SetReadDeadline(time.Now().Add(d))
	line, err := c.lines.ReadString('\n')
	if err != nil {
		t.Fatalf("read a line: %v (got %q, stderr %q)", err, line, c.stderr.String())
	}
	return line
}

// waitExit waits up to d for the process and returns its exit code.
func (c *collector) waitExit(t *testing.T, d time.Duration) int {
	t.Helper()
	select {
	case err := <-c.exited:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ee.ExitCode() < 0 {
				t.Fatalf("process died from a signal: %v", ee)
			}
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(d):
		t.Fatalf("process still running after %v", d)
		return -1
	}
}

func parseEnv(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", line, err)
	}
	return m
}

// `bun src/cli.ts collect --stdio --demo` writes valid NDJSON: hello, snapshot, events
func TestCollectStdioDemo(t *testing.T) {
	c := startCollector(t, "collect", "--stdio", "--demo", "--label", "t")
	start := time.Now()
	hello := parseEnv(t, c.readLine(t, 5*time.Second))
	if hello["kind"] != "hello" || hello["demo"] != true || hello["host"] != "t" || hello["v"] != float64(1) || hello["seq"] != float64(0) {
		t.Fatalf("first line = %v", hello)
	}
	snap := parseEnv(t, c.readLine(t, 5*time.Second))
	if snap["kind"] != "snapshot" || snap["seq"] != float64(0) {
		t.Fatalf("second line = %v", snap)
	}
	last := float64(0)
	upsert := false
	for i := 0; i < 3 || !upsert; i++ {
		if i > 200 {
			t.Fatal("no agent.upsert in 200 lines")
		}
		env := parseEnv(t, c.readLine(t, 3*time.Second))
		seq, _ := env["seq"].(float64)
		if seq <= last {
			t.Fatalf("seq %v after %v", seq, last)
		}
		last = seq
		if env["kind"] == "agent.upsert" {
			upsert = true
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("events took %v", d)
	}
	c.cmd.Process.Signal(syscall.SIGTERM)
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
}

// collect --stdio without --label uses the host name
func TestCollectStdioHostName(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	c := startCollector(t, "collect", "--stdio", "--demo")
	hello := parseEnv(t, c.readLine(t, 5*time.Second))
	if hello["host"] != want {
		t.Fatalf("host = %v, want %q", hello["host"], want)
	}
	c.cmd.Process.Signal(syscall.SIGINT)
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
}

// usage errors print to stderr and exit with code 2
func TestCollectUsageErrors(t *testing.T) {
	for _, argv := range [][]string{{"collect"}, {"--nope"}, {"collect", "--stdio", "--remote", "h"}} {
		var out, errb bytes.Buffer
		cmd := exec.Command(binPath, argv...)
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Fatalf("%q: err = %v, want exit 2", argv, err)
		}
		if !strings.HasPrefix(errb.String(), "agent-view: ") || !strings.Contains(strings.ToLower(errb.String()), "usage") {
			t.Fatalf("%q: stderr = %q", argv, errb.String())
		}
		if out.Len() != 0 {
			t.Fatalf("%q: stdout = %q, want empty", argv, out.String())
		}
	}
}

// The reader goes away: the next write fails, and the process exits 0 within 2 s.
func TestCollectStdioClosedStdout(t *testing.T) {
	c := startCollector(t, "collect", "--stdio", "--demo", "--label", "t")
	for i := 0; i < 3; i++ {
		c.readLine(t, 5*time.Second)
	}
	c.stdout.Close()
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
	if s := c.stderr.String(); strings.Contains(s, "panic") || strings.Contains(s, "goroutine") {
		t.Fatalf("stderr = %q", s)
	}
}

func TestCollectStdioSIGTERM(t *testing.T) {
	c := startCollector(t, "collect", "--stdio", "--demo", "--label", "t")
	c.readLine(t, 5*time.Second)
	c.cmd.Process.Signal(syscall.SIGTERM)
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
}

// A reader that never reads: SIGTERM still ends the process within 2 s.
func TestCollectStdioStalledReaderSIGTERM(t *testing.T) {
	c := startCollector(t, "collect", "--stdio", "--demo", "--demo-scale", "stress", "--label", "t")
	time.Sleep(1500 * time.Millisecond) // let the pipe fill; never read
	c.cmd.Process.Signal(syscall.SIGTERM)
	if code := c.waitExit(t, 2*time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, c.stderr.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

// The first write error stops the collector and gives exit code 0.
func TestRunCollectWriteError(t *testing.T) {
	args := cli.Args{Mode: "collect", Demo: true, Label: "t"}
	done := make(chan int, 1)
	go func() { done <- runCollect(args, failWriter{}, io.Discard, make(chan os.Signal)) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runCollect did not return")
	}
}

// A signal on the channel stops runCollect with exit code 0.
func TestRunCollectSignal(t *testing.T) {
	args := cli.Args{Mode: "collect", Demo: true, Label: "t"}
	signals := make(chan os.Signal, 1)
	var out bytes.Buffer
	pr, pw := io.Pipe()
	go io.Copy(&out, pr)
	done := make(chan int, 1)
	go func() { done <- runCollect(args, pw, io.Discard, signals) }()
	time.Sleep(200 * time.Millisecond)
	signals <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runCollect did not return")
	}
	pw.Close()
}
