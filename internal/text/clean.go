// Package text sanitizes strings from agent data before they reach the cell grid.
package text

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const newlineMark = "⏎"

// Clean makes s safe for a one-line field. It replaces invalid UTF-8 with
// U+FFFD, removes ANSI escape sequences, C0 controls (a tab becomes one
// space), DEL, C1 controls, and bidi override and isolate characters, and
// replaces each newline with ⏎.
func Clean(s string) string {
	return clean(ansi.Strip(strings.ToValidUTF8(s, "�")), newlineMark)
}

// CleanLines applies the same cleaning to each line of s and drops the lines
// that end up empty.
func CleanLines(s string) []string {
	s = ansi.Strip(strings.ToValidUTF8(s, "�"))
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = clean(line, ""); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// clean removes control and bidi runes from an already stripped, valid UTF-8
// string. A newline becomes nl.
func clean(s, nl string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(nl)
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20, r >= 0x7f && r <= 0x9f,
			r >= 0x202a && r <= 0x202e,
			r >= 0x2066 && r <= 0x2069:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
