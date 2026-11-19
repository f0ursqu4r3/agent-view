package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/feed"
)

func pipe(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r
}

func TestRunNotATerminal(t *testing.T) {
	var errb bytes.Buffer
	code := run(nil, pipe(t), pipe(t), &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if got := strings.TrimSpace(errb.String()); got != notTerminal {
		t.Fatalf("message = %q, want %q", got, notTerminal)
	}
}

func TestRunUsageError(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"--port", "1"}, pipe(t), pipe(t), &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	out := errb.String()
	if !strings.Contains(out, "--port is only valid with serve or audit") || !strings.Contains(out, "usage:") {
		t.Fatalf("stderr = %q", out)
	}
}

// The TUI mode checks --remote like serve: a bad host is a usage error, exit 2.
func TestRunTUIBadRemote(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"--remote", "a b"}, pipe(t), pipe(t), &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if out := errb.String(); !strings.Contains(out, "Invalid --remote value") || !strings.Contains(out, "usage:") {
		t.Fatalf("stderr = %q", out)
	}
}

func TestBothTerminals(t *testing.T) {
	in, out := pipe(t), pipe(t)
	want := map[uintptr]bool{in.Fd(): true, out.Fd(): true}
	isTTY := func(fd uintptr) bool { return want[fd] }
	if !bothTerminals(in, out, isTTY) {
		t.Fatal("both terminals: got false")
	}
	want[in.Fd()] = false
	if bothTerminals(in, out, isTTY) {
		t.Fatal("stdin not a terminal: got true")
	}
	want[in.Fd()], want[out.Fd()] = true, false
	if bothTerminals(in, out, isTTY) {
		t.Fatal("stdout not a terminal: got true")
	}
}

func TestRunDevNullIsNotATerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	var errb bytes.Buffer
	if code := run(nil, null, null, &errb); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if got := strings.TrimSpace(errb.String()); got != notTerminal {
		t.Fatalf("message = %q, want %q", got, notTerminal)
	}
}

// NO_COLOR set to any non-empty value turns color off, "0" and "yes" too.
func TestNoColor(t *testing.T) {
	for val, want := range map[string]bool{"": false, "1": true, "yes": true, "0": true, "false": true} {
		t.Setenv("NO_COLOR", val)
		if got := noColor(); got != want {
			t.Errorf("NO_COLOR=%q: noColor() = %v, want %v", val, got, want)
		}
	}
}

// rest returns the lines the program never took, in order: a line sent as
// the program ended, then the lines still queued after the stop.
func TestDiagWriterRest(t *testing.T) {
	w := newDiagWriter()
	sent := make(chan tea.Msg, 4)
	ctx, cancel := context.WithCancel(context.Background())
	w.start(ctx, func(m tea.Msg) { sent <- m })

	w.line("taken")
	if m := w.filter(nil, <-sent); m != feed.DiagMsg("taken") {
		t.Fatalf("filter = %#v, want DiagMsg(taken)", m)
	}
	w.Write([]byte("dropped\n")) // sent, but the program had ended
	<-sent
	cancel()
	w.line("queued")
	if got, want := w.rest(), []string{"dropped", "queued"}; !slices.Equal(got, want) {
		t.Fatalf("rest = %q, want %q", got, want)
	}
}

// A panic after the bridge started and before the program runs does not
// hang: the sources stop, stderr is restored, and the panic text prints once
// on the real stderr.
func TestRunTUIPanicBeforeRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(isolatedEnv(t.TempDir()), helperEnv+"=prerun-panic")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("the helper hung; stderr %q", errb.String())
	}
	if err == nil {
		t.Fatal("helper exited 0, want a panic exit")
	}
	if n := strings.Count(errb.String(), "PRE-RUN-PANIC-MARKER"); n != 1 {
		t.Fatalf("stderr has the panic message %d times, want 1: %q", n, errb.String())
	}
}
