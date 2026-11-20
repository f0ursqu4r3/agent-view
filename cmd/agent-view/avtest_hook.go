//go:build avtest

package main

// This file is compiled only into the binary that the end-to-end tests build
// with "go build -tags avtest". It makes the running TUI write to its own
// stderr and through the log package, which no test outside the process can
// do (TestTUIProcessOwnWrites). It also makes the program send a signal to
// itself just before the sources start or just before the program runs,
// which no test outside the process can time (TestTUISignalBeforeStart…,
// TestTUISignalBeforeRun…). A normal build does not contain it, and it reads
// no environment variable of the test.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// longLogPad is the length of the filler in the log line. It is longer than
// the 4,096 bytes that the stderr capture keeps of a line, so only a log line
// that goes to the diagnostics directly arrives whole.
const longLogPad = 5000

func init() {
	start, run := beforeStart, beforeRun
	beforeStart = func(requested <-chan struct{}) {
		start(requested)
		// The signal reaches the watcher on another goroutine: wait for it,
		// so that the run sees it before the sources start.
		if signalWhenAsked("signal-before-start") {
			select {
			case <-requested:
			case <-time.After(5 * time.Second):
			}
		}
	}
	beforeRun = func() {
		run()
		go writeWhenAsked()
		signalWhenAsked("signal-before-run")
	}
}

// hookDir returns <HOME>/avtest-hook.
func hookDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "avtest-hook"), nil
}

// hookSignals are the texts that the signal files hold.
var hookSignals = map[string]syscall.Signal{"TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "HUP": syscall.SIGHUP}

// signalWhenAsked reads <HOME>/avtest-hook/<name>. When the file exists, it
// sends the signal that the file names (TERM, INT or HUP) to this process and
// returns true.
func signalWhenAsked(name string) bool {
	dir, err := hookDir()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	sig, ok := hookSignals[strings.TrimSpace(string(b))]
	return ok && syscall.Kill(os.Getpid(), sig) == nil
}

// writeWhenAsked waits for the file <HOME>/avtest-hook/go. Its three lines
// are a text for os.Stderr, and the head and the tail of one long log line.
// It writes them while the TUI runs, then creates <HOME>/avtest-hook/done.
func writeWhenAsked() {
	dir, err := hookDir()
	if err != nil {
		return
	}
	var asked []byte
	for {
		if asked, err = os.ReadFile(filepath.Join(dir, "go")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	lines := bytes.Split(bytes.TrimSpace(asked), []byte("\n"))
	if len(lines) != 3 {
		return
	}
	fmt.Fprintln(os.Stderr, string(lines[0]))
	log.Printf("%s%s%s", lines[1], strings.Repeat("-", longLogPad), lines[2])
	_ = os.WriteFile(filepath.Join(dir, "done"), nil, 0o600)
}
