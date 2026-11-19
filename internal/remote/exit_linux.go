//go:build linux

package remote

import "golang.org/x/sys/unix"

// waitExit waits until the child pid has exited, and does not reap it. It
// reports false when it cannot tell; the child may still run then.
func waitExit(pid int) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}
