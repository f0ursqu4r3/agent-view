package wire

import (
	"runtime"
	"time"
)

// FormatISO formats ms (Unix milliseconds) like JavaScript's
// new Date(ms).toISOString(), for years 0 to 9999.
func FormatISO(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// NowMs returns the current time in Unix milliseconds.
func NowMs() int64 {
	return time.Now().UnixMilli()
}

// Platform returns the platform name as Node's process.platform and
// process.arch give it, joined by a hyphen.
func Platform() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x64"
	case "386":
		arch = "ia32"
	}
	return runtime.GOOS + "-" + arch
}
