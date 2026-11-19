package text

import (
	"strings"
	"unicode"
)

// JSTrim is String.prototype.trim: it drops white space and line terminators
// at both ends.
func JSTrim(s string) string {
	return strings.TrimFunc(s, func(c rune) bool {
		return c == '\uFEFF' || (c != '\u0085' && unicode.IsSpace(c))
	})
}
