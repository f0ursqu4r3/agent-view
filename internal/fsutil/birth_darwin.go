package fsutil

import "syscall"

// BirthMs returns the birth time of path in Unix milliseconds, as Node's
// fs.lstatSync(path).birthtimeMs gives it: a symlink is not followed. It
// returns 0 when path cannot be read.
func BirthMs(path string) float64 {
	var st syscall.Stat_t
	for {
		err := syscall.Lstat(path, &st)
		if err == nil {
			break
		}
		if err != syscall.EINTR {
			return 0
		}
	}
	return float64(st.Birthtimespec.Sec)*1000 + float64(st.Birthtimespec.Nsec)/1e6
}
