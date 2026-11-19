// Package parity compares the Go collector, hub and audit with results that
// the former TypeScript implementation recorded once. The results are stored
// in testdata/golden/parity/ (see its README.md). The tests need no Bun.
package parity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-view/internal/demo"
	"agent-view/internal/wire"
)

const (
	// quietTime ends a run: this long with no line other than coverage.
	quietTime = 3 * time.Second
	// maxRunTime ends a run that never goes quiet.
	maxRunTime = 60 * time.Second
	// exitWait bounds the wait for a process after SIGTERM.
	exitWait = 5 * time.Second
	// demoStartMs is the virtual clock's start, 2026-01-01T00:00:00Z.
	demoStartMs = 1767225600000
)

var (
	// repoRoot is the repository root. The Go binary runs from it.
	repoRoot string
	// goBin is the agent-view binary that TestMain builds.
	goBin string
)

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	repoRoot = root
	dir, err := os.MkdirTemp("", "agent-view-parity-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	goBin = filepath.Join(dir, "agent-view")
	build := exec.Command("go", "build", "-o", goBin, "./cmd/agent-view")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// childEnv is the test's environment without CODEX_HOME and
// CLAUDE_CONFIG_DIR, and with HOME set to home.
func childEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home)
}

// run is the output of one collector process.
type run struct {
	lines    []string
	stderr   string
	err      error
	timedOut bool // the run never went quiet
}

// kindOf returns the kind of an NDJSON line, or "" when it has none.
func kindOf(line string) string {
	var head struct{ Kind string }
	_ = json.Unmarshal([]byte(line), &head)
	return head.Kind
}

// runCollector runs argv from the repository root with HOME set to home. It
// reads stdout until quietTime passes with no line other than coverage
// (maxRunTime at most), sends SIGTERM, and reads the rest until the process
// exits.
func runCollector(argv []string, home string) run {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = repoRoot
	cmd.Env = childEnv(home)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return run{err: err}
	}
	if err := cmd.Start(); err != nil {
		return run{err: err}
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadString('\n')
			if strings.HasSuffix(line, "\n") {
				lines <- strings.TrimSuffix(line, "\n")
			}
			if err != nil {
				return
			}
		}
	}()

	var r run
	quiet := time.NewTimer(quietTime)
	defer quiet.Stop()
	deadline := time.NewTimer(maxRunTime)
	defer deadline.Stop()
read:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break read // the process ended on its own; Wait reports why
			}
			r.lines = append(r.lines, line)
			if kindOf(line) != "coverage" {
				quiet.Reset(quietTime)
			}
		case <-quiet.C:
			break read
		case <-deadline.C:
			r.timedOut = true
			break read
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	exited := time.NewTimer(exitWait)
	defer exited.Stop()
drain:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break drain
			}
			r.lines = append(r.lines, line)
		case <-exited.C:
			_ = cmd.Process.Kill()
			go func() {
				for range lines {
				}
			}()
			break drain
		}
	}
	r.err = cmd.Wait()
	r.stderr = stderr.String()
	return r
}

// copyFile copies src to dst, creating dst's directory.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// fixtureHome builds a HOME whose Claude and Codex roots hold the fixture
// logs. Each file gets its own mtime inside the last hour, in a fixed order:
// source by source, then by path without the .jsonl suffix, so a session log
// comes before its subagents/ directory. The collectors read the oldest first.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	sources := []struct{ from, to string }{
		{"testdata/fixtures/claude", ".claude/projects/fixtures"},
		{"testdata/fixtures/codex", ".codex/sessions/fixtures"},
		{"testdata/parity/claude", ".claude/projects/parity"},
		{"testdata/parity/codex", ".codex/sessions/parity"},
	}
	var files []string // destination paths, in read order
	for _, s := range sources {
		from := filepath.Join(repoRoot, filepath.FromSlash(s.from))
		var rels []string
		err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
				rel, err := filepath.Rel(from, p)
				if err != nil {
					return err
				}
				rels = append(rels, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rels) == 0 {
			t.Fatalf("no logs in %s", s.from)
		}
		sort.Slice(rels, func(i, j int) bool {
			return strings.TrimSuffix(rels[i], ".jsonl") < strings.TrimSuffix(rels[j], ".jsonl")
		})
		for _, rel := range rels {
			dst := filepath.Join(home, filepath.FromSlash(s.to), rel)
			if err := copyFile(filepath.Join(from, rel), dst); err != nil {
				t.Fatal(err)
			}
			files = append(files, dst)
		}
	}
	base := time.Now().Add(-50 * time.Minute)
	for i, f := range files {
		mtime := base.Add(time.Duration(i) * 10 * time.Second)
		if err := os.Chtimes(f, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// eventValue is a demo event as the demo goldens store it: the event fields
// and ts, without the envelope's v, host and seq.
func eventValue(t *testing.T, e wire.SourceEvent) envelope {
	t.Helper()
	b, err := wire.Envelope{Ts: e.Ts, Ev: e.Ev}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var v envelope
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	delete(v, "v")
	delete(v, "host")
	delete(v, "seq")
	return v
}

// goDemoTrace runs the Go demo on a FakeScheduler for ms and returns its
// events as the demo goldens store them.
func goDemoTrace(t *testing.T, scale string, ms float64) []envelope {
	t.Helper()
	var emitted []wire.SourceEvent
	sched := demo.NewFakeScheduler(demoStartMs)
	src := demo.New(demo.Options{Host: "parity", Scale: scale, Sched: sched, Emit: func(batch []wire.SourceEvent) {
		emitted = append(emitted, batch...)
	}})
	src.Start()
	sched.Advance(ms)
	src.Stop()
	var events []envelope
	for _, e := range emitted {
		events = append(events, eventValue(t, e))
	}
	return events
}
