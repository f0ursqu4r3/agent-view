package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
