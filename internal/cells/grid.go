// Package cells holds the cell grid that cards draw into.
package cells

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

const ellipsis = "…"

// Style is the look of one cell.
type Style struct {
	Role          Role
	Bold, Inverse bool
}

// Cell is one terminal cell. W is the grapheme width (1 or 2); W 0 marks the
// right half of a wide grapheme, and G is empty there.
type Cell struct {
	G string
	W uint8
	S Style
}

// Grid is a W by H block of cells in row-major order.
type Grid struct {
	W, H int
	C    []Cell
}

// BoxKind selects the border glyphs of Box.
type BoxKind uint8

const (
	Heavy BoxKind = iota
	Round
	Square
)

// boxRunes holds top-left, top-right, bottom-left, bottom-right, horizontal, vertical.
var boxRunes = [...][6]string{
	Heavy:  {"┏", "┓", "┗", "┛", "━", "┃"},
	Round:  {"╭", "╮", "╰", "╯", "─", "│"},
	Square: {"┌", "┐", "└", "┘", "─", "│"},
}

var blank = Cell{G: " ", W: 1}

// New returns a grid of spaces in RText.
func New(w, h int) *Grid {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	g := &Grid{W: w, H: h, C: make([]Cell, w*h)}
	for i := range g.C {
		g.C[i] = blank
	}
	return g
}

func (g *Grid) in(x, y int) bool { return x >= 0 && y >= 0 && x < g.W && y < g.H }

// put stores c at (x, y), which must be in bounds. It turns the other half of
// any wide grapheme that c overwrites into a space, so no half glyph remains.
func (g *Grid) put(x, y int, c Cell) {
	i := y*g.W + x
	old := g.C[i]
	if old.W == 0 && c.W != 0 && x > 0 {
		g.C[i-1] = Cell{G: " ", W: 1, S: g.C[i-1].S}
	}
	if old.W == 2 && x+1 < g.W {
		g.C[i+1] = Cell{G: " ", W: 1, S: g.C[i+1].S}
	}
	g.C[i] = c
}

// putWide stores a grapheme of width w at (x, y). Parts outside the grid are
// clipped; a wide grapheme cut by an edge leaves a space in the visible half.
func (g *Grid) putWide(x, y, w int, s string, st Style) {
	if w == 1 {
		if g.in(x, y) {
			g.put(x, y, Cell{G: s, W: 1, S: st})
		}
		return
	}
	if g.in(x, y) && g.in(x+1, y) {
		g.put(x, y, Cell{G: s, W: 2, S: st})
		g.put(x+1, y, Cell{W: 0, S: st})
		return
	}
	for dx := 0; dx < w; dx++ {
		if g.in(x+dx, y) {
			g.put(x+dx, y, Cell{G: " ", W: 1, S: st})
		}
	}
}

// glyph returns what Text writes for one grapheme and its width in cells,
// or width 0 for a grapheme that Text skips (a control or a zero-width
// grapheme). Bubble Tea measures text with wcwidth unless the terminal
// reports mode 2027, which common terminals do not. A grapheme whose
// grapheme width and wcwidth differ (an emoji with a variation selector or a
// skin tone, a ZWJ sequence) would move every cell after it on the screen,
// so it becomes its first rune when that rune's two widths agree, else "?".
// Either way the glyph has the same width by both measures, 1 or 2.
func glyph(gr string) (string, int) {
	if len(gr) == 1 && gr[0] < utf8.RuneSelf { // ASCII, the common case
		if gr[0] < 0x20 || gr[0] == 0x7f {
			return "", 0
		}
		return gr, 1
	}
	if strings.IndexFunc(gr, unicode.IsControl) >= 0 {
		return "", 0
	}
	w := ansi.StringWidth(gr)
	if w == ansi.StringWidthWc(gr) && w <= 2 {
		return gr, w
	}
	r, _ := utf8.DecodeRuneInString(gr)
	first := string(r)
	if w := ansi.StringWidth(first); w >= 1 && w <= 2 && w == ansi.StringWidthWc(first) {
		return first, w
	}
	return "?", 1
}

// GraphemeWidth returns the cells Text uses for one grapheme: 0 for a
// grapheme it skips, else 1 or 2.
func GraphemeWidth(gr string) int {
	_, w := glyph(gr)
	return w
}

// Width returns the layout width of s as Text counts it.
func Width(s string) int {
	n := 0
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		n += GraphemeWidth(gr.Str())
	}
	return n
}

// Text writes s at (x, y) using at most max cells and returns the number of
// cells used. If s is wider than max, the last used cell is "…"; a wide
// grapheme that does not fit also becomes "…". The count is the layout width,
// so it includes cells that fall outside the grid and were clipped. Control
// graphemes and zero-width graphemes are skipped. A grapheme whose grapheme
// width and wcwidth differ is replaced (see glyph), so each cell holds a
// glyph as wide as the cells it takes by either measure.
func (g *Grid) Text(x, y, max int, s string, st Style) int {
	if max <= 0 {
		return 0
	}
	type piece struct {
		s string
		w int
	}
	var ps []piece
	total := 0
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		str, w := glyph(gr.Str())
		if w <= 0 {
			continue
		}
		ps = append(ps, piece{str, w})
		total += w
	}
	limit := max
	if total > max {
		limit = max - 1 // leave one cell for the ellipsis
	}
	used := 0
	for _, p := range ps {
		if used+p.w > limit {
			break
		}
		g.putWide(x+used, y, p.w, p.s, st)
		used += p.w
	}
	if total > max {
		g.putWide(x+used, y, 1, ellipsis, st)
		used++
	}
	return used
}

// Box draws a border of the given size. The inside is left unchanged.
func (g *Grid) Box(x, y, w, h int, kind BoxKind, st Style) {
	if w < 2 || h < 2 {
		return
	}
	r := boxRunes[kind]
	set := func(cx, cy int, s string) {
		if g.in(cx, cy) {
			g.put(cx, cy, Cell{G: s, W: 1, S: st})
		}
	}
	for cx := x + 1; cx < x+w-1; cx++ {
		set(cx, y, r[4])
		set(cx, y+h-1, r[4])
	}
	for cy := y + 1; cy < y+h-1; cy++ {
		set(x, cy, r[5])
		set(x+w-1, cy, r[5])
	}
	set(x, y, r[0])
	set(x+w-1, y, r[1])
	set(x, y+h-1, r[2])
	set(x+w-1, y+h-1, r[3])
}

// Blit copies src onto g with its top-left corner at (x, y). Cells outside g
// are clipped.
func (g *Grid) Blit(src *Grid, x, y int) {
	for sy := 0; sy < src.H; sy++ {
		for sx := 0; sx < src.W; sx++ {
			dx, dy := x+sx, y+sy
			if !g.in(dx, dy) {
				continue
			}
			c := src.C[sy*src.W+sx]
			switch {
			case c.W == 2 && !g.in(dx+1, dy):
				c = Cell{G: " ", W: 1, S: c.S}
			case c.W == 0 && !g.in(dx-1, dy):
				c = Cell{G: " ", W: 1, S: c.S}
			}
			g.put(dx, dy, c)
		}
	}
}

// ClipRows keeps only rows top..top+n-1, clamped to the grid.
func (g *Grid) ClipRows(top, n int) {
	if top < 0 {
		top = 0
	}
	if top > g.H {
		top = g.H
	}
	if n < 0 {
		n = 0
	}
	if top+n > g.H {
		n = g.H - top
	}
	g.C = append([]Cell(nil), g.C[top*g.W:(top+n)*g.W]...)
	g.H = n
}

// Dim recolors every cell to RDim, except RFaint, RDim, and RFail, which stay,
// and clears Bold on every cell. Inverse is kept.
func (g *Grid) Dim() {
	for i := range g.C {
		g.C[i].S.Bold = false
		switch g.C[i].S.Role {
		case RDim, RFaint, RFail:
		default:
			g.C[i].S.Role = RDim
		}
	}
}

// Plain returns the grid text, one line per row. The right half of a wide
// grapheme adds nothing, so a row with wide graphemes has fewer characters
// than cells.
func Plain(g *Grid) string {
	var b strings.Builder
	for y := 0; y < g.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for _, c := range g.C[y*g.W : (y+1)*g.W] {
			b.WriteString(c.G)
		}
	}
	return b.String()
}

// Mask returns one style letter per cell, one line per row, so it lines up
// with the cell columns (both halves of a wide grapheme get a letter). The
// letters t d f b r e c s a o x k z stand for the roles in order; a letter is
// uppercase when bold, and # marks an inverse cell.
func Mask(g *Grid) string {
	var b strings.Builder
	for y := 0; y < g.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for _, c := range g.C[y*g.W : (y+1)*g.W] {
			l := maskLetters[c.S.Role]
			switch {
			case c.S.Inverse:
				l = '#'
			case c.S.Bold:
				l -= 'a' - 'A'
			}
			b.WriteByte(l)
		}
	}
	return b.String()
}
