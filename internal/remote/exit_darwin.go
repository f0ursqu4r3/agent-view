//go:build darwin

package remote

import "golang.org/x/sys/unix"

// waitExit waits until the child pid has exited, and does not reap it. It
// reports false when it cannot tell; the child may still run then.
//
// It waits for NOTE_EXIT on a kqueue. A child that has already exited (a
// zombie) cannot be added: ESRCH. It does not use waitid, which on Darwin
// also returns for a stopped child (Go issue 19314).
func waitExit(pid int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	add := []unix.Kevent_t{{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT,
	}}
	for {
		_, err := unix.Kevent(kq, add, nil, nil)
		if err == unix.ESRCH {
			return true
		}
		if err == nil {
			break
		}
		if err != unix.EINTR {
			return false
		}
	}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if err == nil && n > 0 {
			return true
		}
		if err != nil && err != unix.EINTR {
			return false
		}
	}
}
