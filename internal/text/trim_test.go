package text

import "testing"

// JSTrim is String.prototype.trim: white space and line terminators go at
// both ends. U+FEFF counts, U+0085 does not, and the middle stays.
func TestJSTrim(t *testing.T) {
	const (
		bom = string(rune(0xFEFF))
		nel = string(rune(0x0085))
		ls  = string(rune(0x2028))
		ps  = string(rune(0x2029))
		nbs = string(rune(0x00A0))
		em  = string(rune(0x2003))
		ide = string(rune(0x3000))
	)
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain", "abc", "abc"},
		{"spaces and tabs", " \t abc \t ", "abc"},
		{"line terminators", "\r\n" + ls + ps + "abc\n", "abc"},
		{"bom", bom + "abc" + bom, "abc"},
		{"no-break and unicode space", nbs + em + "abc" + ide, "abc"},
		{"next line is kept", nel + "abc" + nel, nel + "abc" + nel},
		{"middle is kept", "a b\tc", "a b\tc"},
		{"only white space", " \t\r\n" + bom, ""},
	}
	for _, c := range cases {
		if got := JSTrim(c.in); got != c.want {
			t.Errorf("%s: JSTrim(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
