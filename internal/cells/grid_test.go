package cells

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var plain = Style{Role: RText}

func TestTextTruncatesWithEllipsis(t *testing.T) {
	g := New(5, 1)
	n := g.Text(0, 0, 5, "abcdefgh", plain)
	if got := Plain(g); got != "abcd…" {
		t.Fatalf("Plain = %q, want %q", got, "abcd…")
	}
	if n != 5 {
		t.Fatalf("written = %d, want 5", n)
	}
}

func TestTextFitsWithoutEllipsis(t *testing.T) {
	g := New(8, 1)
	n := g.Text(1, 0, 5, "abcde", plain)
	if got := Plain(g); got != " abcde  " || n != 5 {
		t.Fatalf("Plain = %q written = %d", got, n)
	}
}

func TestTextWideGraphemes(t *testing.T) {
	g := New(5, 1)
	g.Text(0, 0, 5, "日本語", plain)
	if got := Plain(g); got != "日本…" {
		t.Fatalf("Plain = %q, want %q", got, "日本…")
	}
	if g.C[0].W != 2 || g.C[1].W != 0 || g.C[2].W != 2 || g.C[3].W != 0 || g.C[4].W != 1 {
		t.Fatalf("widths = %d %d %d %d %d", g.C[0].W, g.C[1].W, g.C[2].W, g.C[3].W, g.C[4].W)
	}

	g = New(4, 1)
	g.Text(0, 0, 4, "a日本", plain)
	if got := Plain(g); got != "a日…" {
		t.Fatalf("Plain = %q, want %q", got, "a日…")
	}

	// A wide grapheme that does not fit becomes the ellipsis.
	g = New(4, 1)
	n := g.Text(0, 0, 4, "日本語", plain)
	if got := Plain(g); got != "日… " || n != 3 {
		t.Fatalf("Plain = %q written = %d", got, n)
	}
}

// Bubble Tea measures text with wcwidth unless the terminal reports mode
// 2027, which common terminals do not. A grapheme whose grapheme width and
// wcwidth differ is written as its first rune when that rune's two widths
// agree, else as "?", so every written cell has the width the renderer
// gives it and the border after the text stays in its column.
func TestTextWidthsMatchTheRenderer(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"⚠️ x", "⚠ x"},      // U+26A0 U+FE0F: grapheme 2, wcwidth 1
		{"❤️ x", "❤ x"},      // U+2764 U+FE0F: grapheme 2, wcwidth 1
		{"👍🏽 x", "👍 x"},      // a skin tone: grapheme 2, wcwidth 4
		{"👨‍👩‍👧‍👦 x", "👨 x"}, // a ZWJ family: grapheme 2, wcwidth 8
		{"ೳ x", "? x"},       // one rune: grapheme 1, wcwidth 0
		{"漢字 x", "漢字 x"},     // CJK: both 2
		{"é x", "é x"},     // a combining mark: both 1
	} {
		g := New(14, 3)
		g.Box(0, 0, 14, 3, Square, plain)
		g.Text(1, 1, 12, tt.in, plain)
		row := g.C[g.W : 2*g.W]
		var text strings.Builder
		cellsW := 0
		for x, c := range row {
			text.WriteString(c.G)
			cellsW += int(c.W)
			if c.W != 0 && ansi.StringWidthWc(c.G) != int(c.W) {
				t.Errorf("%q: cell %d holds %q of width %d, wcwidth %d", tt.in, x, c.G, c.W, ansi.StringWidthWc(c.G))
			}
		}
		if got := ansi.StringWidthWc(text.String()); got != cellsW || got != g.W {
			t.Errorf("%q: row %q has wcwidth %d, cells %d, want %d", tt.in, text.String(), got, cellsW, g.W)
		}
		if got := strings.TrimRight(text.String()[len("│"):], " │"); got != tt.want {
			t.Errorf("%q: wrote %q, want %q", tt.in, got, tt.want)
		}
		// The right border stays in the last column on every line.
		for y, line := range strings.Split(Plain(g), "\n") {
			if w := ansi.StringWidthWc(line); w != g.W || !strings.HasSuffix(line, "┐") && !strings.HasSuffix(line, "│") && !strings.HasSuffix(line, "┘") {
				t.Errorf("%q: line %d %q has wcwidth %d, want %d and a border at the end", tt.in, y, line, w, g.W)
			}
		}
	}
}

func TestTextClipsOutOfBounds(t *testing.T) {
	g := New(6, 2)
	g.Text(-3, 0, 10, "abcdefghij", plain)
	g.Text(g.W+2, 0, 10, "abc", plain)
	g.Text(0, -1, 5, "abc", plain)
	g.Text(0, 5, 5, "abc", plain)
	g.Text(-1, 1, 5, "日本", plain)
	g.Text(5, 1, 5, "日", plain)
	g.Text(0, 0, 0, "abc", plain)
	if strings.ContainsRune(Plain(g), '\x00') {
		t.Fatal("grid holds NUL")
	}
	// A wide grapheme cut by the edge leaves a space, not half a glyph.
	row := strings.Split(Plain(g), "\n")[1]
	if row != " 本   " {
		t.Fatalf("row 1 = %q", row)
	}
}

func TestTextOverwriteRepairsWideHalves(t *testing.T) {
	g := New(4, 1)
	g.Text(0, 0, 4, "日本", plain)
	g.Text(1, 0, 1, "x", plain)
	if got := Plain(g); got != " x本" {
		t.Fatalf("Plain = %q", got)
	}
}

func TestTextDropsControlGraphemes(t *testing.T) {
	g := New(5, 1)
	g.Text(0, 0, 5, "a\x1b[b", plain)
	if strings.ContainsRune(Plain(g), '\x1b') {
		t.Fatal("ESC reached the grid")
	}
}

func TestBoxKinds(t *testing.T) {
	cases := []struct {
		kind BoxKind
		want string
	}{
		{Heavy, "┏━━┓\n┃  ┃\n┗━━┛"},
		{Round, "╭──╮\n│  │\n╰──╯"},
		{Square, "┌──┐\n│  │\n└──┘"},
	}
	for _, c := range cases {
		g := New(4, 3)
		g.Box(0, 0, 4, 3, c.kind, plain)
		if got := Plain(g); got != c.want {
			t.Errorf("kind %d:\n%s\nwant\n%s", c.kind, got, c.want)
		}
	}
}

func TestBoxClipsAndIgnoresTinySizes(t *testing.T) {
	g := New(3, 3)
	g.Box(-1, -1, 6, 6, Heavy, plain)
	g.Box(0, 0, 1, 1, Heavy, plain)
	g.Box(0, 0, 0, 0, Heavy, plain)
}

func TestMaskLetters(t *testing.T) {
	g := New(4, 1)
	g.Text(0, 0, 1, "a", Style{Role: RRead, Bold: true})
	g.Text(1, 0, 1, "b", Style{Role: RFail, Inverse: true})
	g.Text(2, 0, 1, "c", Style{Role: RFail})
	g.Text(3, 0, 1, "d", Style{Role: RClaude})
	if got := Mask(g); got != "R#zo" {
		t.Fatalf("Mask = %q, want %q", got, "R#zo")
	}
}

func TestMaskAllRoles(t *testing.T) {
	g := New(len(palette), 1)
	for r := range palette {
		g.Text(r, 0, 1, "x", Style{Role: Role(r)})
	}
	if got := Mask(g); got != "tdfbrecsaoxkzmpuipppuuuiiggg" {
		t.Fatalf("Mask = %q", got)
	}
}

func TestMaskColumnsMatchWideCells(t *testing.T) {
	g := New(3, 1)
	g.Text(0, 0, 2, "日", Style{Role: RRead})
	if got := Mask(g); got != "rrt" {
		t.Fatalf("Mask = %q", got)
	}
}

func TestRoleOf(t *testing.T) {
	cases := map[string]Role{
		"read": RRead, "edit": REdit, "write": REdit, "command": RCommand,
		"search": RSearch, "agent": RAgent, "other": RText, "": RText, "zzz": RText,
	}
	for in, want := range cases {
		if got := RoleOf(in); got != want {
			t.Errorf("RoleOf(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestBlit(t *testing.T) {
	dst := New(6, 3)
	src := New(3, 2)
	src.Text(0, 0, 3, "abc", Style{Role: RRead})
	src.Text(0, 1, 3, "日x", Style{Role: REdit})
	dst.Blit(src, 1, 1)
	want := "      \n abc  \n 日x  "
	if got := Plain(dst); got != want {
		t.Fatalf("Plain = %q, want %q", got, want)
	}
	dst.Blit(src, 5, 2)  // clipped right: wide half cut, no panic
	dst.Blit(src, -2, 0) // clipped left
	dst.Blit(src, 0, 9)  // fully outside
	if got := strings.Split(Plain(dst), "\n")[0]; got != "c     " {
		t.Fatalf("row 0 after left clip = %q", got)
	}
}

func TestBlitCutsWideGraphemeAtEdges(t *testing.T) {
	src := New(2, 1)
	src.Text(0, 0, 2, "日", plain)
	dst := New(3, 1)
	dst.Blit(src, 2, 0)
	if got := Plain(dst); got != "   " {
		t.Fatalf("right cut: %q", got)
	}
	dst = New(3, 1)
	dst.Blit(src, -1, 0)
	if got := Plain(dst); got != "   " {
		t.Fatalf("left cut: %q", got)
	}
}

func TestClipRows(t *testing.T) {
	g := New(2, 5)
	for y := 0; y < 5; y++ {
		g.Text(0, y, 2, string(rune('a'+y)), plain)
	}
	g.ClipRows(1, 3)
	if g.H != 3 || len(g.C) != 6 {
		t.Fatalf("H = %d len = %d", g.H, len(g.C))
	}
	if got := Plain(g); got != "b \nc \nd " {
		t.Fatalf("Plain = %q", got)
	}
	g.ClipRows(2, 10) // clamps
	if got := Plain(g); got != "d " || g.H != 1 {
		t.Fatalf("Plain = %q H = %d", got, g.H)
	}
	g.ClipRows(-4, 0)
	if g.H != 0 || len(g.C) != 0 {
		t.Fatalf("H = %d", g.H)
	}
}

func TestDim(t *testing.T) {
	g := New(6, 1)
	g.Text(0, 0, 1, "a", Style{Role: RRead, Bold: true})
	g.Text(1, 0, 1, "b", Style{Role: RFail, Bold: true})
	g.Text(2, 0, 1, "c", Style{Role: RFaint})
	g.Text(3, 0, 1, "d", Style{Role: RBright, Bold: true})
	g.Text(4, 0, 1, "e", Style{Role: RRead, Inverse: true})
	g.Text(5, 0, 1, "~", Style{Role: RFoam})
	g.Dim()
	// Bold is cleared on every cell; Inverse is kept.
	if got := Mask(g); got != "dzfd#i" || g.C[5].S.Role != RFoam {
		t.Fatalf("dimmed mask = %q, background role = %v", got, g.C[5].S.Role)
	}
}
