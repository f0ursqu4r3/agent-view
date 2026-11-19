//go:build unix

// Package quiet keeps writes to the process's stderr off the terminal while
// the TUI runs.
package quiet

import (
	"bytes"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// maxLine is the most bytes of one line that reach onLine; the rest of a
// longer line is dropped.
const maxLine = 4096

// drainWait is how long restore waits for the reader to reach the end of the
// pipe. A child process that inherited fd 2 keeps the pipe open; restore then
// closes the read end.
const drainWait = 200 * time.Millisecond

// CaptureStderr points fd 2 at a pipe, so writes through os.Stderr, direct
// writes to fd 2 and the Go runtime's own messages do not reach the
// terminal. A goroutine splits the pipe into lines, each cut to 4,096 bytes
// and without its "\n" or "\r\n", and calls onLine for each one in order.
// onLine must not write to stderr.
//
// While fd 2 is captured, the runtime also writes an unrecovered panic or a
// fatal error of any goroutine to the original fd 2 (debug.SetCrashOutput),
// so the crash text is not lost; the terminal is not restored first in that
// case, which cannot be done in-process.
//
// restore puts the original fd 2 back, turns that crash output off, closes
// the pipe and waits for the reader, so every captured line has reached
// onLine when it returns. It may be called more than once.
func CaptureStderr(onLine func(string)) (restore func(), err error) {
	saved, err := unix.Dup(2)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(saved)
	if err := setCrashOutput(saved); err != nil {
		unix.Close(saved)
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		unix.Close(saved)
		return nil, err
	}
	if err := unix.Dup2(int(w.Fd()), 2); err != nil {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		unix.Close(saved)
		r.Close()
		w.Close()
		return nil, err
	}
	w.Close() // fd 2 holds the write end now

	done := make(chan struct{})
	go func() {
		defer close(done)
		readLines(r, onLine)
	}()

	restore = sync.OnceFunc(func() {
		// Dup2 closes the write end at fd 2, so the reader sees the end of
		// the pipe once no child holds it.
		_ = unix.Dup2(saved, 2)
		unix.Close(saved)
		// fd 2 is the terminal again: a crash output too would print a
		// panic twice.
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		select {
		case <-done:
		case <-time.After(drainWait):
		}
		r.Close()
		<-done
	})
	return restore, nil
}

// setCrashOutput makes the runtime write crash text to a copy of fd. The
// runtime keeps its own close-on-exec duplicate, so the copy is closed here.
func setCrashOutput(fd int) error {
	dup, err := unix.Dup(fd)
	if err != nil {
		return err
	}
	unix.CloseOnExec(dup)
	f := os.NewFile(uintptr(dup), "stderr")
	defer f.Close()
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// readLines reads r to its end or error and calls onLine for each line; a
// last line without "\n" counts too.
func readLines(r io.Reader, onLine func(string)) {
	buf := make([]byte, 32*1024)
	var line []byte
	skipping := false // the current line is longer than maxLine
	emit := func() {
		onLine(string(bytes.TrimSuffix(line, []byte("\r"))))
		line = line[:0]
	}
	for {
		n, err := r.Read(buf)
		chunk := buf[:n]
		for len(chunk) > 0 {
			i := bytes.IndexByte(chunk, '\n')
			part := chunk
			if i >= 0 {
				part = chunk[:i]
			}
			if !skipping {
				room := maxLine - len(line)
				if len(part) > room {
					part = part[:room]
					skipping = true
				}
				line = append(line, part...)
				if skipping {
					emit()
				}
			}
			if i < 0 {
				break
			}
			if !skipping {
				emit()
			}
			skipping = false
			chunk = chunk[i+1:]
		}
		if err != nil {
			if len(line) > 0 {
				emit()
			}
			return
		}
	}
}
