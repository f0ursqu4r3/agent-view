// Package cards draws the board's cards into cell grids. Each card is a pure
// function of its input.
package cards

import "unicode/utf16"

// fnv1a32 hashes the UTF-16 code units of s, as the prototype's
// charCodeAt loop does.
func fnv1a32(s string) uint32 {
	h := uint32(2166136261)
	for _, u := range utf16.Encode([]rune(s)) {
		h = (h ^ uint32(u)) * 16777619
	}
	return h
}

// mulberry32 returns a generator of values in [0, 1).
func mulberry32(seed uint32) func() float64 {
	a := seed
	return func() float64 {
		a += 0x6D2B79F5
		t := (a ^ a>>15) * (1 | a)
		t = (t + (t^t>>7)*(61|t)) ^ t
		return float64(t^t>>14) / 4294967296
	}
}

// Sprite returns the 8 × 8 mirrored bitmap of an agent ID as 4 lines of
// 8 half-block cells. The same ID always gives the same sprite.
func Sprite(agentID string) [4]string {
	rnd := mulberry32(fnv1a32(agentID))
	var bits [8][8]bool
	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			v := rnd() < 0.55
			switch {
			case y == 0 && x < 2:
				v = false
			case y == 3 && x == 2:
				v = false
			case y == 3 && x == 1:
				v = true
			case (y == 2 || y == 4) && x == 2:
				v = true
			}
			bits[y][x] = v
			bits[y][7-x] = v
		}
	}
	var rows [4]string
	for ry := 0; ry < 4; ry++ {
		row := make([]rune, 8)
		for cx := 0; cx < 8; cx++ {
			top, bottom := bits[ry*2][cx], bits[ry*2+1][cx]
			switch {
			case top && bottom:
				row[cx] = '█'
			case top:
				row[cx] = '▀'
			case bottom:
				row[cx] = '▄'
			default:
				row[cx] = ' '
			}
		}
		rows[ry] = string(row)
	}
	return rows
}
