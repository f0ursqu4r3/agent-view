package cells

import (
	"strings"
	"testing"
)

func TestEncodeRunsAndResets(t *testing.T) {
	g := New(4, 2)
	g.Text(0, 0, 2, "ab", Style{Role: RRead})
	g.Text(2, 0, 2, "cd", Style{Role: RFail, Bold: true})
	g.Text(0, 1, 1, "e", Style{Role: RText, Inverse: true})
	out := Encode(g)

	for i := 0; i < len(out); i++ {
		b := out[i]
		if b < 0x20 && b != 0x1b && b != '\n' {
			t.Fatalf("byte %#x at %d", b, i)
		}
	}
	if n := strings.Count(out, "\x1b[38;2;97;175;239m"); n != 1 {
		t.Fatalf("read SGR count = %d in %q", n, out)
	}
	want := "\x1b[38;2;97;175;239mab\x1b[0m\x1b[38;2;224;108;117;1mcd\x1b[0m\n" +
		"\x1b[38;2;197;200;198;7me\x1b[0m\x1b[38;2;197;200;198m   \x1b[0m"
	if out != want {
		t.Fatalf("Encode:\n%q\nwant\n%q", out, want)
	}
}

func TestEncodeSkipsWideRightHalf(t *testing.T) {
	g := New(3, 1)
	g.Text(0, 0, 3, "日a", Style{Role: RText})
	out := Encode(g)
	if !strings.Contains(out, "日a") {
		t.Fatalf("Encode = %q", out)
	}
}

func TestEncodeEmptyGrid(t *testing.T) {
	if got := Encode(New(0, 0)); got != "" {
		t.Fatalf("Encode = %q", got)
	}
}

// Without color, the encoder writes no color code: the dim and faint roles
// become SGR 2 (faint), and bold and inverse stay. Runs join on equal SGR,
// so roles that differ only in color share one run.
func TestEncodeNoColor(t *testing.T) {
	g := New(6, 2)
	g.Text(0, 0, 2, "ab", Style{Role: RRead})
	g.Text(2, 0, 1, "c", Style{Role: RDim})
	g.Text(3, 0, 1, "d", Style{Role: RFaint, Bold: true})
	g.Text(4, 0, 1, "e", Style{Role: RFail, Inverse: true})
	g.Text(5, 0, 1, "f", Style{Role: RCommand, Inverse: true})
	g.Text(0, 1, 2, "gh", Style{Role: RText, Bold: true})
	out := EncodeNoColor(g)
	if strings.Contains(out, "\x1b[38") || strings.Contains(out, ";38") {
		t.Fatalf("color code in %q", out)
	}
	want := "ab\x1b[2mc\x1b[0m\x1b[2;1md\x1b[0m\x1b[7mef\x1b[0m\n" +
		"\x1b[1mgh\x1b[0m    "
	if out != want {
		t.Fatalf("EncodeNoColor:\n%q\nwant\n%q", out, want)
	}
	// The color encoder of the same grid keeps the colors, and dim is a color there.
	if c := Encode(g); !strings.Contains(c, "\x1b[38;2;92;99;112mc") || strings.Contains(c, "\x1b[2m") {
		t.Fatalf("Encode = %q, want the dim color and no SGR 2", c)
	}
}

func TestEncodeNoColorSkipsWideRightHalf(t *testing.T) {
	g := New(3, 1)
	g.Text(0, 0, 3, "日a", Style{Role: RDim})
	if out := EncodeNoColor(g); out != "\x1b[2m日a\x1b[0m" {
		t.Fatalf("EncodeNoColor = %q", out)
	}
	if out := EncodeNoColor(New(0, 0)); out != "" {
		t.Fatalf("EncodeNoColor of an empty grid = %q", out)
	}
}
