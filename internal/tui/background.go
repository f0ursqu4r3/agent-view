package tui

import (
	"math"

	"agent-view/internal/cells"
)

// The background is a function of viewport and phase. Repainting the same
// frame (for example, after a feed event) never advances the simulation.
func (m motionState) drawBackground(g *cells.Grid) {
	if g.W == 0 || g.H == 0 {
		return
	}
	switch m.background {
	case glyphRain:
		drawStreams(g, m.phase)
	case scanWave:
		drawScan(g, m.phase)
	case plasma:
		drawPlasma(g, m.phase)
	case fire:
		drawFire(g, m.phase)
	case raindrops:
		drawRipples(g, m.phase)
	}
}

type shade struct {
	glyph string
	ink   cells.Role
}

func (s shade) put(g *cells.Grid, x, y int) {
	g.C[y*g.W+x] = cells.Cell{G: s.glyph, W: 1, S: cells.Style{Role: s.ink}}
}

// A stable spatial hash distributes streams, sparks and drop impacts.
func noise(x, y int) uint32 {
	n := uint32(x)*0x9e3779b1 + uint32(y)*0x85ebca77
	n ^= n >> 16
	n *= 0x7feb352d
	n ^= n >> 15
	return n
}

func drawStreams(g *cells.Grid, phase int) {
	const alphabet = "0123456789ABCDEF:;=<>/|+*"
	for lane := 0; lane*2 < g.W; lane++ {
		seed := noise(lane, 37)
		x := lane*2 + int(seed%2)
		if x >= g.W {
			continue
		}
		length := 5 + int(seed>>5%uint32(max(6, min(g.H, 22))))
		travel := g.H + length + int(seed>>14%13)
		head := (int(seed%7919) + phase*(2+int(seed>>10%5))/4) % travel
		for y := max(0, head-length+1); y <= min(g.H-1, head); y++ {
			dist := head - y
			// Most characters persist as the head passes; a few change
			// independently to give the column a digital flicker.
			code := noise(x, y)
			index := (code + uint32(phase/(8+int(code%19)))) % uint32(len(alphabet))
			ink := cells.RForest
			switch {
			case dist == 0:
				ink = cells.RPhosphor
			case dist < 1+length/3:
				ink = cells.RMatrix
			case dist > length*3/4 && code%3 == 0:
				continue
			}
			shade{alphabet[index : index+1], ink}.put(g, x, y)
		}
	}
}

// Braille gives the phosphor trace two horizontal and four vertical samples
// per terminal cell. Neighbouring samples share dots on steep slopes.
var braille = func() [256]string {
	var glyphs [256]string
	for i := range glyphs {
		glyphs[i] = string(rune(0x2800 + i))
	}
	return glyphs
}()

func drawScan(g *cells.Grid, phase int) {
	dots := [2][4]byte{{1, 2, 4, 64}, {8, 16, 32, 128}}
	head := phase * max(1, g.W/55) % g.W
	signal := func(sample int) int {
		x := float64(sample) * 0.052
		t := float64(phase) * 0.047
		wave := math.Sin(x-t) + 0.46*math.Sin(2.2*x+t) + 0.18*math.Sin(4*x-t)
		return int((0.5 + 0.24*wave) * float64(g.H*4-1))
	}
	column := make([]byte, g.H)
	prev := signal(-1)
	for x := 0; x < g.W; x++ {
		if x%10 == 0 {
			for y := 2; y < g.H; y += 4 {
				shade{"·", cells.RDeepWater}.put(g, x, y)
			}
		}
		lo, hi := g.H-1, 0
		for side := range 2 {
			y := signal(2*x + side)
			for dot := min(prev, y); dot <= max(prev, y); dot++ {
				column[dot/4] |= dots[side][dot%4]
			}
			lo, hi = min(lo, prev/4, y/4), max(hi, prev/4, y/4)
			prev = y
		}
		age := (head - x + g.W) % g.W
		ink := cells.RRain
		switch {
		case age < max(1, g.W/30):
			ink = cells.RPhosphor
		case age < max(2, g.W/4):
			ink = cells.RMatrix
		case age > g.W/2:
			ink = cells.RForest
		}
		for y := lo; y <= hi; y++ {
			shade{braille[column[y]], ink}.put(g, x, y)
			column[y] = 0
		}
	}
}

func drawPlasma(g *cells.Grid, phase int) {
	ramp := [...]shade{{".", cells.RDeepWater}, {":", cells.RRain}, {"~", cells.RIndigo},
		{"=", cells.RPlasma}, {"+", cells.RViolet}, {"*", cells.ROrchid}, {"o", cells.RViolet},
		{"=", cells.RPlasma}, {"~", cells.RIndigo}, {":", cells.RRain}}
	t := float64(phase) * 0.052
	for y := 0; y < g.H; y++ {
		v := float64(y) * 0.19
		bend := 1.55 * math.Sin(v*0.78+t*0.8)
		for x := 0; x < g.W; x++ {
			u := float64(x) * 0.097
			field := math.Sin(u+bend-t) + math.Sin(v+t+1.35*math.Sin(u*0.72-t*0.55))
			field += 0.65 * math.Sin(u*0.55+v*0.8+t*0.43)
			// Alternate dark troughs and colored ridges across the field.
			band := int((field+2.7)*3.8) % (len(ramp) + 2)
			if band < len(ramp) {
				ramp[band].put(g, x, y)
			}
		}
	}
}

func drawFire(g *cells.Grid, phase int) {
	ramp := [...]shade{
		{".", cells.RCoal}, {":", cells.REmber}, {";", cells.REmber},
		{"+", cells.RFlame}, {"x", cells.RFlame}, {"%", cells.RGold}, {"#", cells.RGold},
	}
	t := float64(phase) * 0.13
	depth := min(g.H, max(5, g.H*2/3))
	for dy := 0; dy < depth; dy++ {
		h := float64(dy) / float64(depth)
		// Sample the flame higher up as time advances. Taller tongues
		// bend farther in the wind and develop gaps as they cool.
		lift := float64(dy)*0.30 - t
		wind := h * (2.8*math.Sin(lift*0.63) + 1.6*math.Sin(t*0.42))
		for x := 0; x < g.W; x++ {
			u := float64(x)*0.16 + wind
			tongue := math.Sin(u+0.7*math.Sin(lift)) + 0.48*math.Sin(2.7*u+lift)
			// Advect a finer turbulent texture through the broader plumes.
			grain := fireNoise(0.43*float64(x)+wind, 1.23*lift)
			heat := 0.80 - 1.18*h + (0.19+0.15*h)*tongue + 0.09*math.Sin(1.8*lift-u) + 0.32*(grain-0.5)
			if heat > 0.05 {
				ramp[min(len(ramp)-1, int(heat*float64(len(ramp))))].put(g, x, g.H-1-dy)
			}
		}
	}
	for source := 0; source < g.W; source += 8 {
		seed := noise(source, 19)
		cycle := max(12, depth+8)
		age := phase + int(seed%97)
		step := age % cycle
		y := g.H - 1 - depth/4 - step
		x := source + int(noise(source, age/cycle)%8) + int(2*math.Sin(float64(step)*0.24))
		if x >= 0 && x < g.W && y >= 0 && step < depth*3/4 {
			spark := shade{"'", cells.RFlame}
			if step < 3 {
				spark = shade{"*", cells.RGold}
			}
			spark.put(g, x, y)
		}
	}
}

// Bilinear value noise keeps eddies coherent between adjacent cells and frames.
func fireNoise(x, y float64) float64 {
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(ix), y-float64(iy)
	a, b := float64(noise(ix, iy)>>24), float64(noise(ix+1, iy)>>24)
	c, d := float64(noise(ix, iy+1)>>24), float64(noise(ix+1, iy+1)>>24)
	return ((a+(b-a)*fx)*(1-fy) + (c+(d-c)*fx)*fy) / 255
}

func drawRipples(g *cells.Grid, phase int) {
	// Add signed amplitudes so intersecting rings can reinforce or cancel.
	water := make([]float32, len(g.C))
	for top := -10; top < g.H+10; top += 11 {
		for left := -18; left < g.W+18; left += 19 {
			seed := noise(left, top)
			period := 38 + int(seed%29)
			clock := phase + int(seed>>8%127)
			age := clock % period
			if age >= 34 {
				continue
			}
			center := noise(int(seed%65536), clock/period)
			cx, cy := left+int(center%19), top+int(center>>8%11)
			radius := float64(age) * 0.52
			reach := int(radius) + 2
			for y := max(0, cy-reach/2-1); y <= min(g.H-1, cy+reach/2+1); y++ {
				for x := max(0, cx-reach); x <= min(g.W-1, cx+reach); x++ {
					dx, dy := float64(x-cx), float64(2*(y-cy))
					d := radius - math.Sqrt(dx*dx+dy*dy)
					if d < -1 || d > 6 {
						continue
					}
					envelope := (1 - float64(age)/36) * (1 - math.Abs(d-1.5)/5)
					water[y*g.W+x] += float32(math.Cos(d*1.45) * envelope)
				}
			}
		}
	}
	for i, v := range water {
		var s shade
		switch {
		case v > 0.45:
			s = shade{"o", cells.RFoam}
		case v > 0.23:
			s = shade{"~", cells.RRain}
		case v > 0.10:
			s = shade{"·", cells.RRain}
		case v < -0.16:
			s = shade{":", cells.RDeepWater}
		default:
			continue
		}
		s.put(g, i%g.W, i/g.W)
	}
}
