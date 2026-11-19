//go:build !darwin && !linux

package fsutil

// BirthMs returns 0: the birth time is read only on macOS and Linux.
func BirthMs(path string) float64 { return 0 }
