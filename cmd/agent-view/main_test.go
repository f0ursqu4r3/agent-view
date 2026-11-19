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
