package text

import "testing"

func TestSliceUTF16(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"ab😀c", 3, "ab"},
		{"ab😀c", 4, "ab😀"},
		{"ab😀c", 5, "ab😀c"},
		{"ab😀c", 100, "ab😀c"},
		{"日本語", 2, "日本"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"", 3, ""},
	}
	for _, c := range cases {
		if got := SliceUTF16(c.in, c.n); got != c.want {
			t.Errorf("SliceUTF16(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
