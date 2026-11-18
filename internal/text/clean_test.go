package text

import (
	"reflect"
	"testing"
)

func TestCleanRemovesEscapesAndControls(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"csi and osc", "a\x1b[31mb\x1b]0;title\x07c", "abc"},
		{"bidi override", "x\u202ey", "xy"},
		{"bidi isolate", "x\u2066y\u2069z", "xyz"},
		{"c1 control", "\u009b2J", "2J"},
		{"tab", "a\tb", "a b"},
		{"newline", "l1\nl2", "l1⏎l2"},
		{"crlf", "l1\r\nl2", "l1⏎l2"},
		{"invalid utf8", "\xff", "\ufffd"},
		{"del and nul", "a\x7fb\x00c", "abc"},
		{"lone esc", "a\x1b", "a"},
		{"wide text kept", "日本語 ok", "日本語 ok"},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("%s: Clean(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestCleanLinesDropsEmpty(t *testing.T) {
	got := CleanLines("one\n\n\x1b[2Ktwo\n")
	want := []string{"one", "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanLines = %q, want %q", got, want)
	}
	if got := CleanLines("a\r\n\r\nb\x07"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("CleanLines crlf = %q", got)
	}
	if got := CleanLines(""); len(got) != 0 {
		t.Fatalf("CleanLines(\"\") = %q, want empty", got)
	}
}
