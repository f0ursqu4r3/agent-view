//go:build unix

package quiet

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// The tests run each case in a helper process (this test binary run again
// with helperEnv set), so the fd 2 of the test process is never redirected.
const helperEnv = "QUIET_TEST_HELPER"

func TestMain(m *testing.M) {
	if name := os.Getenv(helperEnv); name != "" {
		helpers[name]()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helpers write what onLine receives to stdout as "line <n>: <text>".
var helpers = map[string]func(){
	"lines": func() {
		restore := capture()
		fmt.Fprintln(os.Stderr, "via os.Stderr")
		syscall.Write(2, []byte("via fd 2\npartial"))
		restore()
		restore() // idempotent
		fmt.Fprintln(os.Stderr, "after restore")
	},
	"long": func() {
		restore := capture()
		os.Stderr.Write(append(bytes.Repeat([]byte("x"), 100_000), '\n'))
		fmt.Fprintln(os.Stderr, "next line")
		restore()
	},
	"panic": func() {
		restore := capture()
		defer restore()
		panic("QUIET-PANIC-MARKER")
	},
}

// capture starts CaptureStderr with an onLine that prints to stdout.
func capture() func() {
	n := 0
	restore, err := CaptureStderr(func(s string) {
		n++
		fmt.Printf("line %d: %s\n", n, s)
	})
	if err != nil {
		fmt.Printf("CaptureStderr: %v\n", err)
		os.Exit(3)
	}
	return restore
}

// runHelper runs the named helper and returns its stdout, stderr and error.
func runHelper(t *testing.T, name string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+name)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// Writes through os.Stderr and directly to fd 2 reach onLine, the last
// partial line too; after restore a write reaches the real stderr.
func TestCaptureStderrLines(t *testing.T) {
	out, errs, err := runHelper(t, "lines")
	if err != nil {
		t.Fatalf("helper: %v\nstdout %q\nstderr %q", err, out, errs)
	}
	want := "line 1: via os.Stderr\nline 2: via fd 2\nline 3: partial\n"
	if out != want {
		t.Fatalf("onLine got %q, want %q", out, want)
	}
	if errs != "after restore\n" {
		t.Fatalf("real stderr = %q, want only the write after restore", errs)
	}
}

// A 100 KB line gives one line of at most 4,096 bytes, and the next line
// still arrives.
func TestCaptureStderrLongLine(t *testing.T) {
	out, errs, err := runHelper(t, "long")
	if err != nil {
		t.Fatalf("helper: %v\nstderr %q", err, errs)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2: %.200q", len(lines), out)
	}
	first := strings.TrimPrefix(lines[0], "line 1: ")
	if len(first) == 0 || len(first) > 4096 || strings.Trim(first, "x") != "" {
		t.Fatalf("first line has %d bytes, want 1 to 4096 of x", len(first))
	}
	if lines[1] != "line 2: next line" {
		t.Fatalf("second line = %q", lines[1])
	}
	if errs != "" {
		t.Fatalf("real stderr = %q, want nothing", errs)
	}
}

// A panic after the capture, with restore deferred, prints its message on
// the real stderr: restore runs before the runtime prints the panic.
func TestCapturePanicPrintsAfterRestore(t *testing.T) {
	out, errs, err := runHelper(t, "panic")
	if err == nil {
		t.Fatal("helper exited 0, want a panic exit")
	}
	if !strings.Contains(errs, "panic: QUIET-PANIC-MARKER") {
		t.Fatalf("real stderr = %q, want the panic message", errs)
	}
	if strings.Contains(out, "QUIET-PANIC-MARKER") {
		t.Fatalf("onLine got the panic message: %q", out)
	}
}
