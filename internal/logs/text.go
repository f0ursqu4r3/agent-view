package logs

import (
	"path/filepath"
	"unicode/utf8"
)

// resolveFile is path.resolve for a log field: file must be a non-empty
// string. An absolute path is cleaned. A relative path needs a non-empty cwd
// and joins it; a relative cwd resolves against the process directory.
func resolveFile(cwd *string, file any) (string, bool) {
	f, ok := file.(string)
	if !ok || f == "" {
		return "", false
	}
	if filepath.IsAbs(f) {
		return filepath.Clean(f), true
	}
	if cwd == nil || *cwd == "" {
		return "", false
	}
	p := filepath.Join(*cwd, f)
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", false
		}
		p = abs
	}
	return p, true
}

// isContinuation reports whether b is a UTF-8 continuation byte.
func isContinuation(b byte) bool { return b&0xc0 == 0x80 }

// validUTF8 returns s with each invalid byte replaced by U+FFFD, as
// Buffer.from(s, 'utf8') and toString('utf8') do.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return string([]rune(s))
}
