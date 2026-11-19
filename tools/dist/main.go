// Command dist builds agent-view for the four release targets and checks
// the size of each binary.
//
//	go run ./tools/dist [-out dist] [-targets darwin-arm64,darwin-x64,linux-x64,linux-arm64] [-max-bytes 20971520]
//
// Every target runs go build with -trimpath and -ldflags "-s -w" and writes
// <out>/agent-view-<target>. Linux targets use CGO_ENABLED=0. Darwin targets
// use cgo (the FSEvents file watcher), so they need a Mac as the build host.
//
// The size table goes to stdout, one line per built target. Messages go to
// stderr. The tool also starts the binary that matches the host with
// "collect --stdio --demo --label dist-check" and requires a hello envelope
// as its first line.
//
// Exit codes: 0 all targets built, within the size limit, and the smoke test
// passed; 1 a build, size or smoke failure, or a darwin target on another
// host; 2 a usage error.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"agent-view/internal/schema"
)

// defaultMaxBytes is the spec's size limit: "under 20 MB for each target".
const defaultMaxBytes int64 = 20 << 20

// smokeTimeout is how long the smoke test waits for the first output line.
const smokeTimeout = 10 * time.Second

type target struct {
	name   string // release name, for example darwin-x64
	goos   string
	goarch string
	cgo    bool
}

// targets returns the four release targets in build order.
func targets() []target {
	return []target{
		{"darwin-arm64", "darwin", "arm64", true},
		{"darwin-x64", "darwin", "amd64", true},
		{"linux-x64", "linux", "amd64", false},
		{"linux-arm64", "linux", "arm64", false},
	}
}

// env returns the environment variables that select the target. They go
// after the inherited environment, so they win.
func (t target) env() []string {
	cgo := "0"
	if t.cgo {
		cgo = "1"
	}
	return []string{"GOOS=" + t.goos, "GOARCH=" + t.goarch, "CGO_ENABLED=" + cgo}
}

// isHost reports whether the target runs on a host with this GOOS and GOARCH.
func (t target) isHost(goos, goarch string) bool {
	return t.goos == goos && t.goarch == goarch
}

// checkHost refuses a darwin target on a host that is not a Mac: the FSEvents
// watcher needs cgo and the macOS SDK, and a darwin build without cgo fails.
func checkHost(t target, hostGOOS string) error {
	if t.goos == "darwin" && hostGOOS != "darwin" {
		return errors.New("darwin targets need a Mac (cgo)")
	}
	return nil
}

// atLimit reports whether a binary of this size fails the check.
func atLimit(size, limit int64) bool { return size >= limit }

// checkSizes returns an error that names every target whose binary is at or
// above the limit, or nil when all are below it.
func checkSizes(sizes map[string]int64, limit int64) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(sizes)) {
		if size := sizes[name]; atLimit(size, limit) {
			errs = append(errs, fmt.Errorf("%s: %d bytes is at or above the limit of %d bytes", name, size, limit))
		}
	}
	return errors.Join(errs...)
}

// findModuleRoot returns the nearest directory, from the working directory
// upward, that holds go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found: run the tool inside the agent-view repository")
		}
		dir = parent
	}
}

// buildTarget builds the program for one target into outDir and returns the
// path of the binary. root is the module root, where go build runs.
func buildTarget(t target, root, outDir string) (string, error) {
	if err := checkHost(t, runtime.GOOS); err != nil {
		return "", err
	}
	// go build runs in root, so a relative output path must be made absolute
	// before that.
	outDir, err := filepath.Abs(outDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(outDir, "agent-view-"+t.name)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", path, "./cmd/agent-view")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), t.env()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build failed: %w\n%s", err, bytes.TrimSpace(out))
	}
	// go build applies the umask; the release files are always 0755.
	if err := os.Chmod(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// capture keeps the first limit bytes written to it and drops the rest, so
// the program that writes to it never blocks on a full pipe. When lineSeen is
// not nil, capture closes it as soon as the kept bytes hold a newline.
type capture struct {
	mu       sync.Mutex
	buf      []byte
	limit    int
	lineSeen chan struct{}
	seen     bool
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.limit - len(c.buf); room > 0 {
		kept := p[:min(room, len(p))]
		c.buf = append(c.buf, kept...)
		if c.lineSeen != nil && !c.seen && bytes.IndexByte(kept, '\n') >= 0 {
			c.seen = true
			close(c.lineSeen)
		}
	}
	return len(p), nil
}

// firstLine returns the first complete line written so far.
func (c *capture) firstLine() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := bytes.IndexByte(c.buf, '\n')
	if i < 0 {
		return nil, false
	}
	return slices.Clone(c.buf[:i]), true
}

func (c *capture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(string(c.buf))
}

// smoke starts cmd and reads its stdout until the first line. The line must
// decode as a hello envelope and must come within timeout. smoke then kills
// the process it started and waits for it.
func smoke(cmd *exec.Cmd, timeout time.Duration) error {
	stdout := &capture{limit: 1 << 20, lineSeen: make(chan struct{})}
	stderr := &capture{limit: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second // a child that keeps the pipes open cannot hold Wait for ever
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr error
	waited, timedOut := false, false
	select {
	case <-stdout.lineSeen:
	case waitErr = <-exited:
		waited = true
	case <-timer.C:
		timedOut = true
	}
	if !waited {
		_ = cmd.Process.Kill() // fails only when the process has just ended
		waitErr = <-exited
	}

	line, ok := stdout.firstLine()
	switch {
	case ok:
		if env, decoded := schema.DecodeLine(line); !decoded || env.Kind != "hello" {
			return fmt.Errorf("the first line is not a hello envelope: %.120q", line)
		}
		return nil
	case timedOut:
		return fmt.Errorf("no complete first line within %v", timeout)
	default:
		msg := fmt.Sprintf("the program ended before it printed a first line (%v)", waitErr)
		if s := stderr.text(); s != "" {
			msg += "; stderr: " + s
		}
		return errors.New(msg)
	}
}

// selectTargets turns a comma-separated list of names into targets, in the
// order given. A name that appears twice builds once.
func selectTargets(list string) ([]target, error) {
	known := targets()
	var names []string
	for _, t := range known {
		names = append(names, t.name)
	}
	var selected []target
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		i := slices.IndexFunc(known, func(t target) bool { return t.name == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown target %q (known: %s)", name, strings.Join(names, ", "))
		}
		if !slices.Contains(selected, known[i]) {
			selected = append(selected, known[i])
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no targets (known: %s)", strings.Join(names, ", "))
	}
	return selected, nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main with its streams as parameters. It returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: go run ./tools/dist [flags]")
		fs.PrintDefaults()
	}
	out := fs.String("out", "dist", "output directory")
	list := fs.String("targets", "darwin-arm64,darwin-x64,linux-x64,linux-arm64", "comma-separated targets to build")
	limit := fs.Int64("max-bytes", defaultMaxBytes, "a binary at or above this size fails the check")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "dist: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if *limit < 1 {
		fmt.Fprintf(stderr, "dist: -max-bytes must be at least 1, got %d\n", *limit)
		return 2
	}
	selected, err := selectTargets(*list)
	if err != nil {
		fmt.Fprintf(stderr, "dist: %v\n", err)
		return 2
	}
	root, err := findModuleRoot()
	if err != nil {
		fmt.Fprintf(stderr, "dist: %v\n", err)
		return 1
	}

	if !slices.ContainsFunc(selected, func(t target) bool { return t.isHost(runtime.GOOS, runtime.GOARCH) }) {
		fmt.Fprintf(stderr, "smoke test skipped: no selected target runs on this host (%s/%s)\n", runtime.GOOS, runtime.GOARCH)
	}
	failed := false
	sizes := map[string]int64{}
	for _, t := range selected {
		path, err := buildTarget(t, root, *out)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", t.name, err)
			failed = true
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", t.name, err)
			failed = true
			continue
		}
		sizes[t.name] = info.Size()
		status := "ok"
		if atLimit(info.Size(), *limit) {
			status = "TOO BIG"
		}
		fmt.Fprintf(stdout, "%s  %.1f MB  %s\n", t.name, float64(info.Size())/(1<<20), status)

		if t.isHost(runtime.GOOS, runtime.GOARCH) {
			cmd := exec.Command(path, "collect", "--stdio", "--demo", "--label", "dist-check")
			if err := smoke(cmd, smokeTimeout); err != nil {
				fmt.Fprintf(stderr, "%s: smoke test failed: %v\n", t.name, err)
				failed = true
			} else {
				fmt.Fprintf(stderr, "smoke test passed: %s\n", t.name)
			}
		}
	}
	if err := checkSizes(sizes, *limit); err != nil {
		fmt.Fprintln(stderr, err)
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}
