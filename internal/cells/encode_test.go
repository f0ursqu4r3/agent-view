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
