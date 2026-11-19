package cells

import (
	"strconv"
	"strings"
)

const reset = "\x1b[0m"

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

// sgrNoColor returns the escape sequence that selects the style without
// color, or "" for a plain style: SGR 2 (faint) for the dim and faint roles,
// 1 for bold, 7 for inverse.
func sgrNoColor(s Style) string {
	var p []string
	if s.Role == RDim || s.Role == RFaint {
		p = append(p, "2")
	}
	if s.Bold {
		p = append(p, "1")
	}
	if s.Inverse {
		p = append(p, "7")
	}
	if len(p) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(p, ";") + "m"
}

// Encode turns the grid into a string for Bubble Tea: rows joined by "\n",
// with one SGR sequence per run of equal style and a reset ("\x1b[0m") at each
// style change and at the end of each row. Bubble Tea downsamples the colors to
// the terminal profile. Besides ESC and "\n", the output holds no byte below
// 0x20 as long as the grid holds none, which Text ensures.
func Encode(g *Grid) string { return encode(g, sgr) }

// EncodeNoColor is Encode without color, for NO_COLOR: it writes no color
// code, SGR 2 (faint) for the dim and faint roles, and keeps bold and
// inverse. A run of cells without any of these has no SGR sequence.
func EncodeNoColor(g *Grid) string { return encode(g, sgrNoColor) }

// encode writes the grid with seq selecting each style. A run ends where the
// sequence changes, so styles that look the same share one run.
func encode(g *Grid, seq func(Style) string) string {
	var b strings.Builder
	for y := 0; y < g.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		var style Style
		cur := "" // the sequence in effect, "" for none
		started := false
		for _, c := range g.C[y*g.W : (y+1)*g.W] {
			if c.W == 0 {
				continue
			}
			if !started || c.S != style {
				if s := seq(c.S); !started || s != cur {
					if cur != "" {
						b.WriteString(reset)
					}
					b.WriteString(s)
					cur = s
				}
				style, started = c.S, true
			}
			b.WriteString(c.G)
		}
		if cur != "" {
			b.WriteString(reset)
		}
	}
	return b.String()
}
