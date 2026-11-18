package cells

import (
	"strconv"
	"strings"
)

// sgr returns the escape sequence that selects the style from a reset state.
func sgr(s Style) string {
	c := palette[s.Role]
	out := "\x1b[38;2;" + strconv.Itoa(int(c.r)) + ";" + strconv.Itoa(int(c.g)) + ";" + strconv.Itoa(int(c.b))
	if s.Bold {
		out += ";1"
	}
	if s.Inverse {
		out += ";7"
	}
	return out + "m"
}

// Encode turns the grid into a string for Bubble Tea: rows joined by "\n",
// with one SGR sequence per run of equal style and a reset ("\x1b[0m") at each
// style change and at the end of each row. Bubble Tea downsamples the colors to
// the terminal profile and honors NO_COLOR. Besides ESC and "\n", the output
// holds no byte below 0x20 as long as the grid holds none, which Text ensures.
func Encode(g *Grid) string {
	var b strings.Builder
	for y := 0; y < g.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		var cur Style
		open := false
		for _, c := range g.C[y*g.W : (y+1)*g.W] {
			if c.W == 0 {
				continue
			}
			if !open || c.S != cur {
				if open {
					b.WriteString("\x1b[0m")
				}
				b.WriteString(sgr(c.S))
				cur, open = c.S, true
			}
			b.WriteString(c.G)
		}
		if open {
			b.WriteString("\x1b[0m")
		}
	}
	return b.String()
}
