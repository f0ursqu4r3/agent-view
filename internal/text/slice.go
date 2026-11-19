package text

// SliceUTF16 returns the first n UTF-16 code units of s, as
// String.prototype.slice(0, n) does, except that a surrogate pair is never
// cut: when n falls inside a pair, the pair's first half is dropped too.
func SliceUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	// A rune never has more UTF-16 units than bytes, so a short string fits.
	if len(s) <= n {
		return s
	}
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}
