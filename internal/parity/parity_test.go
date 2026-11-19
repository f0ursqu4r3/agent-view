// Package parity compares the Go collector with the TypeScript collector
// (src/). Every test runs Bun and skips when Bun is missing.
package parity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"agent-view/internal/demo"
	"agent-view/internal/fschange"
	"agent-view/internal/logs"
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
	// localLogs is how many real logs the local mode copies.
	localLogs = 120
)

var (
	// repoRoot is the repository root. Both collectors run from it.
	repoRoot string
	// bunPath is the real Bun executable, or "" when Bun is missing.
	bunPath string
	// goBin is the agent-view binary that TestMain builds when Bun exists.
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
	bunPath = findBun()
	if bunPath == "" {
		return m.Run() // every test skips
	}
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

// findBun returns the real Bun executable. The bun on PATH can be a shim that
// finds the real Bun through HOME, so the path is resolved here, with the real
// environment, before any child runs with a temporary HOME.
func findBun() string {
	shim, err := exec.LookPath("bun")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shim, "-e", "process.stdout.write(process.execPath)").Output()
	if err != nil || len(out) == 0 {
		return shim
	}
	return string(out)
}

func requireBun(t *testing.T) {
	t.Helper()
	if bunPath == "" {
		t.Skip("bun not on PATH")
	}
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

// runBun runs a TypeScript script from the repository root and returns its
// stdout lines.
func runBun(t *testing.T, args ...string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bunPath, args...)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bun %v: %v\n%s", args, err, stderr.String())
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
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

// runBoth runs the Go and the TypeScript collector at the same time on the
// same HOME and returns their outputs. With private set, a failure gives the
// size of stderr, not its text, which can name real projects.
func runBoth(t *testing.T, home string, private bool) (goRun, tsRun run) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Go(func() { goRun = runCollector([]string{goBin, "collect", "--stdio", "--label", "parity"}, home) })
	wg.Go(func() {
		tsRun = runCollector([]string{bunPath, "src/cli.ts", "collect", "--stdio", "--label", "parity"}, home)
	})
	wg.Wait()
	for _, side := range []struct {
		name string
		r    run
	}{{"Go", goRun}, {"TypeScript", tsRun}} {
		switch {
		case side.r.err != nil && private:
			t.Errorf("%s collector: %v (stderr: %d bytes)", side.name, side.r.err, len(side.r.stderr))
		case side.r.err != nil:
			t.Errorf("%s collector: %v (stderr %q)", side.name, side.r.err, side.r.stderr)
		}
		if side.r.timedOut {
			t.Errorf("%s collector never went quiet within %v", side.name, maxRunTime)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return goRun, tsRun
}

// compareRuns normalizes both runs and reports every difference. It returns
// the TypeScript stream.
func compareRuns(t *testing.T, goRun, tsRun run, local bool) stream {
	t.Helper()
	goS, err := normalize(goRun.lines, local)
	if err != nil {
		t.Fatalf("Go output: %v", err)
	}
	tsS, err := normalize(tsRun.lines, local)
	if err != nil {
		t.Fatalf("TypeScript output: %v", err)
	}
	for _, d := range diffStreams(goS, tsS, local) {
		t.Error(d)
	}
	t.Logf("compared %d ordered envelopes, %d source.status, %d timed-out tool.end, and the last coverage",
		len(tsS.ordered), len(tsS.statuses), len(tsS.timeouts))
	return tsS
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
		{"test/fixtures/claude", ".claude/projects/fixtures"},
		{"test/fixtures/codex", ".codex/sessions/fixtures"},
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

// requireKinds fails when the stream lacks a kind, so a run that read no
// logs cannot pass.
func requireKinds(t *testing.T, s stream, kinds ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, e := range s.ordered {
		k, _ := e["kind"].(string)
		seen[k] = true
	}
	for _, k := range kinds {
		if !seen[k] {
			t.Errorf("the TypeScript stream has no %s envelope", k)
		}
	}
	if len(s.statuses) == 0 || len(s.timeouts) == 0 || s.coverage == nil {
		t.Errorf("statuses %d, timeouts %d, coverage %v: want each present", len(s.statuses), len(s.timeouts), s.coverage != nil)
	}
}

// The Go and TypeScript collectors give the same stream for the fixture logs.
func TestParityFixtures(t *testing.T) {
	requireBun(t)
	home := fixtureHome(t)
	goRun, tsRun := runBoth(t, home, false)
	tsS := compareRuns(t, goRun, tsRun, false)
	requireKinds(t, tsS, "hello", "snapshot", "agent.upsert", "agent.status", "tool.start", "tool.output", "tool.end")
}

// localRoot is where a real log root goes inside the temporary HOME, so the
// collectors discover it with the same provider and scope.
func localRoot(home string, r logs.Root) string {
	switch {
	case r.Provider == "claude":
		return filepath.Join(home, ".claude", "projects")
	case r.Scope == "default":
		return filepath.Join(home, ".codex", "sessions")
	default:
		return filepath.Join(home, ".codex-accounts", r.Scope, "sessions")
	}
}

// copyLocalLogs copies the newest real logs (read-only) into home and keeps
// their mtimes. Logs within 1 ms of the previous one move 1 ms later, so the
// read order is fixed. Logs older than 23 hours are left out, so none crosses
// the collectors' 24-hour limit during the run. It returns the count.
func copyLocalLogs(t *testing.T, home string) int {
	t.Helper()
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	roots := logs.DiscoverRoots(os.Getenv, realHome)
	found := logs.ScanLogs(roots, int64(23*time.Hour/time.Millisecond), nil)
	if len(found) > localLogs {
		found = found[:localLogs]
	}
	if len(found) == 0 {
		t.Skip("no real logs from the last 23 hours")
	}
	slices.Reverse(found) // oldest first
	prev := 0.0
	for i, l := range found {
		rel, err := filepath.Rel(l.Path, l.File)
		if err != nil {
			t.Fatalf("log %d: not under its root", i)
		}
		dst := filepath.Join(localRoot(home, l.Root), rel)
		if err := copyFile(l.File, dst); err != nil {
			t.Fatalf("copy log %d: %v", i, withoutPath(err))
		}
		ms := l.MtimeMs
		if i > 0 && ms-prev < 1 {
			ms = prev + 1
		}
		prev = ms
		mtime := time.Unix(0, int64(ms*float64(time.Millisecond)))
		if err := os.Chtimes(dst, mtime, mtime); err != nil {
			t.Fatalf("set the mtime of log %d: %v", i, withoutPath(err))
		}
	}
	return len(found)
}

// withoutPath drops the path from a file error: real log paths name projects.
func withoutPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

// Opt-in (AGENT_VIEW_PARITY_LOCAL=1): the collectors give the same stream for
// a copy of the newest real logs. A mismatch prints only indexes, kinds and
// IDs, never log content.
func TestParityLocal(t *testing.T) {
	requireBun(t)
	if os.Getenv("AGENT_VIEW_PARITY_LOCAL") != "1" {
		t.Skip("set AGENT_VIEW_PARITY_LOCAL=1 to compare a copy of the real logs")
	}
	home := t.TempDir() // removed at the end, the copy with it
	n := copyLocalLogs(t, home)
	goRun, tsRun := runBoth(t, home, true)
	compareRuns(t, goRun, tsRun, true)
	t.Logf("copied %d logs", n)
}

// eventValue is a demo event as the TypeScript script prints it: the event
// fields and ts, without the envelope's v, host and seq.
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

// The Go demo on a FakeScheduler emits the same events as the TypeScript
// DemoSource on a virtual clock, ts included.
func TestParityDemoTrace(t *testing.T) {
	requireBun(t)
	for _, c := range []struct {
		scale string
		ms    float64
	}{{"normal", 60_000}, {"stress", 10_000}} {
		t.Run(c.scale, func(t *testing.T) {
			var tsEvents []envelope
			for i, line := range runBun(t, "test/parity/demo-trace.ts", c.scale, strconv.FormatFloat(c.ms, 'f', -1, 64)) {
				var v envelope
				if err := json.Unmarshal([]byte(line), &v); err != nil {
					t.Fatalf("TypeScript line %d: %v", i, err)
				}
				tsEvents = append(tsEvents, v)
			}

			var emitted []wire.SourceEvent
			sched := demo.NewFakeScheduler(demoStartMs)
			src := demo.New(demo.Options{Host: "parity", Scale: c.scale, Sched: sched, Emit: func(batch []wire.SourceEvent) {
				emitted = append(emitted, batch...)
			}})
			src.Start()
			sched.Advance(c.ms)
			src.Stop()
			var goEvents []envelope
			for _, e := range emitted {
				goEvents = append(goEvents, eventValue(t, e))
			}

			if len(goEvents) != len(tsEvents) {
				t.Errorf("events: Go %d, TypeScript %d", len(goEvents), len(tsEvents))
			}
			bad := 0
			for i := range min(len(goEvents), len(tsEvents)) {
				if reflect.DeepEqual(goEvents[i], tsEvents[i]) {
					continue
				}
				t.Errorf("event %d: Go %s; TypeScript %s", i, canonical(goEvents[i]), canonical(tsEvents[i]))
				if bad++; bad == maxDiffs {
					t.Fatal("more differences not shown")
				}
			}
			if len(tsEvents) < 100 {
				t.Errorf("only %d events in the TypeScript trace", len(tsEvents))
			}
			t.Logf("compared %d events", len(tsEvents))
		})
	}
}

// gitignoreCase is one row of testdata/gitignore-cases.json.
type gitignoreCase struct {
	Name    string            `json:"name"`
	Files   map[string]string `json:"files"`
	Path    string            `json:"path"`
	Ignored bool              `json:"ignored"`
}

// The Go GitignoreMatcher, the TypeScript GitignoreMatcher (ignore 7.0.16)
// and the stored table agree on every row of the gitignore table.
func TestParityGitignore(t *testing.T) {
	requireBun(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "gitignore-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []gitignoreCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	type result struct {
		Name    string `json:"name"`
		Ignored bool   `json:"ignored"`
	}
	lines := runBun(t, "test/parity/ignore-cases.ts")
	if len(lines) != len(cases) {
		t.Fatalf("TypeScript printed %d results for %d rows", len(lines), len(cases))
	}
	for i, c := range cases {
		var ts result
		if err := json.Unmarshal([]byte(lines[i]), &ts); err != nil {
			t.Fatalf("TypeScript line %d: %v", i, err)
		}
		if ts.Name != c.Name {
			t.Fatalf("TypeScript line %d is %q, want %q", i, ts.Name, c.Name)
		}
		dir := t.TempDir()
		for rel, text := range c.Files {
			file := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := fschange.NewGitignoreMatcher(dir).IsIgnored(filepath.Join(dir, filepath.FromSlash(c.Path)))
		if got != ts.Ignored || ts.Ignored != c.Ignored {
			t.Errorf("%s: Go %v, TypeScript %v, table %v", c.Name, got, ts.Ignored, c.Ignored)
		}
	}
}
