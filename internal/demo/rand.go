// Package demo simulates agents for --demo. It ports src/collector/demo.ts.
package demo

// Mulberry32 is a small deterministic generator: the demo looks the same on
// every run. It matches the TypeScript mulberry32 draw for draw.
func Mulberry32(seed uint32) func() float64 {
	a := seed
	return func() float64 {
		a += 0x6d2b79f5
		t := a
		t = (t ^ (t >> 15)) * (t | 1)
		t ^= t + (t^(t>>7))*(t|61)
		return float64(t^(t>>14)) / 4294967296
	}
}
