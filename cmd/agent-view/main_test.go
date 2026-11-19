package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
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
	code := run([]string{"--remote", "h"}, pipe(t), pipe(t), &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	out := errb.String()
	if !strings.Contains(out, "--remote comes in phase 2") || !strings.Contains(out, "usage:") {
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
