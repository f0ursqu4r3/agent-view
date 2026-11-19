package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestErrCode(t *testing.T) {
	cases := map[syscall.Errno]string{
		syscall.ENOENT:  "ENOENT",
		syscall.EACCES:  "EACCES",
		syscall.ENOTDIR: "ENOTDIR",
		syscall.EMFILE:  "EMFILE",
		syscall.ENFILE:  "ENFILE",
		syscall.ENOSPC:  "ENOSPC",
		syscall.EPERM:   "EPERM",
		syscall.ELOOP:   "ELOOP",
	}
	for errno, want := range cases {
		if got := ErrCode(errno); got != want {
			t.Errorf("ErrCode(%v) = %q, want %q", errno, got, want)
		}
		wrapped := fmt.Errorf("outer: %w", &fs.PathError{Op: "open", Path: "/x", Err: errno})
		if got := ErrCode(wrapped); got != want {
			t.Errorf("ErrCode(wrapped %v) = %q, want %q", errno, got, want)
		}
	}
}

func TestErrCodeOther(t *testing.T) {
	if got := ErrCode(fmt.Errorf("boom")); got != "boom" {
		t.Errorf("plain error: %q", got)
	}
	err := &fs.PathError{Op: "read", Path: "/x", Err: syscall.EIO}
	if got := ErrCode(err); got != err.Error() {
		t.Errorf("EIO: %q, want %q", got, err.Error())
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// findProjectRoot returns cwd when no ancestor has .git
func TestFindProjectRootNoGit(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "av-root-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, _ = filepath.EvalSymlinks(dir)
	cwd := filepath.Join(dir, "x", "y")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindProjectRoot(cwd); got != cwd {
		t.Errorf("got %q, want %q", got, cwd)
	}
}

// findProjectRoot returns nearest .git ancestor, else cwd
func TestFindProjectRootNearestGit(t *testing.T) {
	dir := realTemp(t)
	mk := func(p ...string) {
		if err := os.MkdirAll(filepath.Join(p...), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(dir, "repo", ".git")
	mk(dir, "repo", "packages", "app", "src")
	repo := filepath.Join(dir, "repo")
	if got := FindProjectRoot(filepath.Join(repo, "packages", "app", "src")); got != repo {
		t.Errorf("nested: %q", got)
	}
	if got := FindProjectRoot(repo); got != repo {
		t.Errorf("root: %q", got)
	}

	// A worktree has a .git file; the nearest one wins over an outer repo.
	mk(repo, "wt", "sub")
	wt := filepath.Join(repo, "wt")
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindProjectRoot(filepath.Join(wt, "sub")); got != wt {
		t.Errorf("worktree: %q", got)
	}
	if got := FindProjectRoot("/"); got != "/" {
		t.Errorf("slash: %q", got)
	}
}

func TestFindProjectRootKeepsOriginalCwd(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "av-root-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, _ = filepath.EvalSymlinks(dir)
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	messy := filepath.Join(dir, "x") + "/../x/./"
	if got := FindProjectRoot(messy); got != messy {
		t.Errorf("got %q, want the original %q", got, messy)
	}
}

// BirthMs is 0 for a missing path. A new file's birth time is close to its
// modification time, and a symlink has its own birth time (lstat), not its
// target's. Linux file systems without a birth time give 0.
func TestBirthMs(t *testing.T) {
	dir := t.TempDir()
	if got := BirthMs(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("BirthMs(missing) = %v, want 0", got)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	birth := BirthMs(target)
	if birth == 0 {
		if runtime.GOOS == "darwin" {
			t.Fatal("BirthMs(target) = 0 on darwin")
		}
		t.Skip("no birth time on this file system")
	}
	mtime := float64(fi.ModTime().UnixNano()) / 1e6
	if d := mtime - birth; d < -1000 || d > 1000 {
		t.Errorf("BirthMs(target) = %v, mtime %v: more than 1 s apart", birth, mtime)
	}
	time.Sleep(20 * time.Millisecond)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := BirthMs(link); got <= birth {
		t.Errorf("BirthMs(link) = %v, want later than the target's %v", got, birth)
	}
}
