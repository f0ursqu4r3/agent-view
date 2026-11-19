package fsutil

import "golang.org/x/sys/unix"

// BirthMs returns the birth time of path in Unix milliseconds, as Node's
// fs.lstatSync(path).birthtimeMs gives it: a symlink is not followed. It
// returns 0 when path cannot be read or its file system has no birth time.
func BirthMs(path string) float64 {
	var stx unix.Statx_t
	for {
		err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx)
		if err == nil {
			break
		}
		if err != unix.EINTR {
			return 0
		}
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return float64(stx.Btime.Sec)*1000 + float64(stx.Btime.Nsec)/1e6
}
