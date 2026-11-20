//go:build avtest

package main

// This file is compiled only into the binary that TestTUIProcessOwnWrites
// builds with "go build -tags avtest". It makes the running TUI write to its
// own stderr and through the log package, which no test outside the process
// can do. A normal build does not contain it, and it reads no environment
// variable of the test.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// longLogPad is the length of the filler in the log line. It is longer than
// the 4,096 bytes that the stderr capture keeps of a line, so only a log line
// that goes to the diagnostics directly arrives whole.
const longLogPad = 5000

func init() {
	beforeRun = func() { go writeWhenAsked() }
}

// writeWhenAsked waits for the file <HOME>/avtest-hook/go. Its three lines
// are a text for os.Stderr, and the head and the tail of one long log line.
// It writes them while the TUI runs, then creates <HOME>/avtest-hook/done.
func writeWhenAsked() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, "avtest-hook")
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
