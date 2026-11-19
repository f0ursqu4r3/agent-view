package main

import (
	"bytes"
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTargetsEnv(t *testing.T) {
	want := []struct {
		name, goos, goarch, cgo string
	}{
		{"darwin-arm64", "darwin", "arm64", "1"},
		{"darwin-x64", "darwin", "amd64", "1"},
		{"linux-x64", "linux", "amd64", "0"},
		{"linux-arm64", "linux", "arm64", "0"},
	}
	got := targets()
	if len(got) != len(want) {
		t.Fatalf("targets() has %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		tg := got[i]
		if tg.name != w.name || tg.goos != w.goos || tg.goarch != w.goarch {
			t.Errorf("targets()[%d] = %+v, want %+v", i, tg, w)
		}
		if tg.cgo != (w.goos == "darwin") {
			t.Errorf("%s: cgo = %v, want %v", w.name, tg.cgo, w.goos == "darwin")
		}
		env := tg.env()
		for _, kv := range []string{"GOOS=" + w.goos, "GOARCH=" + w.goarch, "CGO_ENABLED=" + w.cgo} {
			if !contains(env, kv) {
				t.Errorf("%s: env %v lacks %s", w.name, env, kv)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestCheckSizes(t *testing.T) {
	const limit = 20 << 20
	if err := checkSizes(map[string]int64{"linux-x64": limit - 1, "darwin-arm64": limit * 99 / 100}, limit); err != nil {
		t.Fatalf("sizes under the limit: %v", err)
	}
	if err := checkSizes(map[string]int64{"linux-x64": 199 * (1 << 20) / 10}, limit); err != nil {
		t.Fatalf("19.9 MiB must pass: %v", err)
	}
	err := checkSizes(map[string]int64{"linux-x64": limit}, limit)
	if err == nil {
		t.Fatal("exactly the limit must fail")
	}
	if !strings.Contains(err.Error(), "linux-x64") {
		t.Errorf("error does not name the target: %v", err)
	}
	err = checkSizes(map[string]int64{"linux-x64": 1, "darwin-x64": limit + 1, "darwin-arm64": limit * 2}, limit)
	if err == nil {
		t.Fatal("sizes over the limit must fail")
	}
	for _, name := range []string{"darwin-x64", "darwin-arm64"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "linux-x64") {
		t.Errorf("error names a target that is within the limit: %v", err)
	}
}

func TestBuildLinuxArm64(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	tg := targetNamed(t, "linux-arm64")
	path, err := buildTarget(tg, root, out)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "agent-view-linux-arm64"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
	if err := checkSizes(map[string]int64{tg.name: info.Size()}, defaultMaxBytes); err != nil {
		t.Error(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		t.Errorf("file does not start with the ELF magic: % x", data[:4])
	}
	requireStripped(t, path)
	info2, err := exec.Command("go", "version", "-m", path).CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m: %v\n%s", err, info2)
	}
	for _, setting := range []string{"-trimpath=true", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"} {
		if !bytes.Contains(info2, []byte(setting)) {
			t.Errorf("go version -m lacks %s:\n%s", setting, info2)
		}
	}
}

// requireStripped checks the effect of -ldflags "-s -w": go version -m does
// not list the linker flags, so the test reads the ELF file instead.
func requireStripped(t *testing.T, path string) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Symbols(); !errors.Is(err, elf.ErrNoSymbols) {
		t.Errorf("the binary has a symbol table (-s missing): %v", err)
	}
	if f.Section(".debug_info") != nil {
		t.Error("the binary has DWARF data (-w missing)")
	}
}

func targetNamed(t *testing.T, name string) target {
	t.Helper()
	for _, tg := range targets() {
		if tg.name == name {
			return tg
		}
	}
	t.Fatalf("no target %q", name)
	return target{}
}

func TestDarwinNeedsMac(t *testing.T) {
	for _, tg := range targets() {
		err := checkHost(tg, "linux")
		if tg.goos == "darwin" {
			if err == nil || !strings.Contains(err.Error(), "darwin targets need a Mac (cgo)") {
				t.Errorf("%s on a linux host: err = %v", tg.name, err)
			}
		} else if err != nil {
			t.Errorf("%s on a linux host: %v", tg.name, err)
		}
		if err := checkHost(tg, "darwin"); err != nil {
			t.Errorf("%s on a darwin host: %v", tg.name, err)
		}
	}
	if runtime.GOOS == "darwin" {
		t.Skip("this host is a Mac: buildTarget has nothing to refuse")
	}
	// On a real non-darwin host, buildTarget refuses before it starts a build
	// (the module root is not read, so an empty one is enough).
	_, err := buildTarget(targetNamed(t, "darwin-arm64"), "", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "darwin targets need a Mac (cgo)") {
		t.Fatalf("buildTarget on %s: err = %v", runtime.GOOS, err)
	}
}

// helperHello is a first line that decodes as a hello envelope.
const helperHello = `{"v":1,"host":"dist-check","seq":1,"ts":"2026-11-19T00:00:00Z","kind":"hello","collectorVersion":"test","platform":"test","demo":true}`

// TestHelperProcess is not a test. The smoke tests start the test binary
// again with DIST_HELPER set, and this function plays the stub program.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("DIST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "hello":
		os.Stdout.WriteString(helperHello + "\n")
	case "snapshot":
		os.Stdout.WriteString(`{"v":1,"host":"h","seq":1,"ts":"2026-11-19T00:00:00Z","kind":"snapshot"}` + "\n")
	case "garbage":
		os.Stdout.WriteString("this is not json\n")
	case "exit":
		os.Exit(3)
	case "silent":
	}
	// Stay alive until the smoke check kills this process. The limit stops a
	// stub that nobody killed.
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func helperCmd(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "DIST_HELPER="+mode)
	return cmd
}

func TestSmokeRejectsBadFirstLine(t *testing.T) {
	t.Run("hello passes", func(t *testing.T) {
		cmd := helperCmd("hello")
		start := time.Now()
		if err := smoke(cmd, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if cmd.ProcessState == nil {
			t.Error("the stub process was not waited for")
		}
		// The stub sleeps for 30 s: a quick return means smoke stopped it.
		if time.Since(start) > 15*time.Second {
			t.Errorf("smoke waited for the stub to end by itself: %v", time.Since(start))
		}
	})
	for _, c := range []struct{ mode, want string }{
		{"snapshot", "not a hello"},
		{"garbage", "not a hello"},
		{"exit", "before"},
		{"silent", "within"},
	} {
		t.Run(c.mode+" fails", func(t *testing.T) {
			cmd := helperCmd(c.mode)
			err := smoke(cmd, 500*time.Millisecond)
			if err == nil {
				t.Fatal("smoke accepted the stub")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q lacks %q", err, c.want)
			}
			if cmd.ProcessState == nil {
				t.Error("the stub process was not waited for")
			}
		})
	}
	t.Run("missing program fails", func(t *testing.T) {
		if err := smoke(exec.Command(filepath.Join(t.TempDir(), "nope")), time.Second); err == nil {
			t.Fatal("smoke accepted a program that does not exist")
		}
	})
}

func TestRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-targets", "bogus"},
		{"-targets", "linux-x64,bogus"},
		{"-targets", ""},
		{"-nope"},
		{"-max-bytes", "0"},
		{"-max-bytes", "abc"},
		{"extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) = %d, want 2; stderr: %s", args, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) wrote to stdout: %s", args, stdout.String())
		}
		if stderr.Len() == 0 {
			t.Errorf("run(%q) wrote no message", args)
		}
	}
	var stderr bytes.Buffer
	run([]string{"-targets", "bogus"}, new(bytes.Buffer), &stderr)
	if !strings.Contains(stderr.String(), `"bogus"`) || !strings.Contains(stderr.String(), "linux-arm64") {
		t.Errorf("the message must name the bad target and the known ones: %s", stderr.String())
	}
}

var tableLine = regexp.MustCompile(`^linux-arm64  \d+\.\d MB  (ok|TOO BIG)$`)

func TestRunTable(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-out", out, "-targets", "linux-arm64"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr.String())
	}
	line := strings.TrimSuffix(stdout.String(), "\n")
	if !tableLine.MatchString(line) || !strings.HasSuffix(line, "  ok") {
		t.Errorf("table = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, "agent-view-linux-arm64")); err != nil {
		t.Error(err)
	}
}

func TestRunTooBig(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-out", out, "-targets", "linux-arm64", "-max-bytes", "1000"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, stderr.String())
	}
	line := strings.TrimSuffix(stdout.String(), "\n")
	if !tableLine.MatchString(line) || !strings.HasSuffix(line, "  TOO BIG") {
		t.Errorf("table = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "linux-arm64") {
		t.Errorf("stderr does not name the target: %s", stderr.String())
	}
}

func TestRunDarwinNeedsMac(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this host is a Mac")
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-out", t.TempDir(), "-targets", "darwin-arm64"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "darwin targets need a Mac (cgo)") {
		t.Errorf("stderr = %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %s", stdout.String())
	}
}

// TestRunHostTarget builds the target of this host and runs the real smoke
// test on the binary: collect --stdio --demo must print a hello.
func TestRunHostTarget(t *testing.T) {
	var host string
	for _, tg := range targets() {
		if tg.isHost(runtime.GOOS, runtime.GOARCH) {
			host = tg.name
		}
	}
	if host == "" {
		t.Skipf("%s/%s is not one of the four targets", runtime.GOOS, runtime.GOARCH)
	}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-out", out, "-targets", host}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "smoke test passed: "+host) {
		t.Errorf("stderr lacks the smoke result: %s", stderr.String())
	}
}
