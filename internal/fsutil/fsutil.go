// Package fsutil holds small file-system helpers that the collector shares.
package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrCode returns the Node-style code of an errno error, also when it is
// wrapped: ENOENT, EACCES, ENOTDIR, EMFILE, ENFILE, ENOSPC, EPERM or ELOOP.
// For any other error it returns err.Error().
func ErrCode(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOENT:
			return "ENOENT"
		case syscall.EACCES:
			return "EACCES"
		case syscall.ENOTDIR:
			return "ENOTDIR"
		case syscall.EMFILE:
			return "EMFILE"
		case syscall.ENFILE:
			return "ENFILE"
		case syscall.ENOSPC:
			return "ENOSPC"
		case syscall.EPERM:
			return "EPERM"
		case syscall.ELOOP:
			return "ELOOP"
		}
	}
	return err.Error()
}

// FindProjectRoot returns the nearest ancestor of cwd (including cwd) that
// holds a .git entry. When no ancestor has one, it returns cwd unchanged.
func FindProjectRoot(cwd string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		dir = filepath.Clean(cwd)
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}
