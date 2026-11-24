package icon

import (
	"image"
	"image/color"
	"math"
)

// The program icon (the Windows executable, its Installed apps entry and
// its notifications) is our own drawing, not a vendor's: a usage gauge on
// a disc split into a warm and a cool half, one meter for two assistants.
// It has no vendor logo, mascot, lettering or brand colour
// (TestProgramColoursAreOurOwn). tools/mkicon writes it to winres/icon.ico.

// Program is the design in use. The runner-up is TwinBars: to use it,
// change this line to "var Program Design = TwinBars" and run
// "go run ./tools/mkicon" in agent/.
var Program Design = SplitFace

// Program icon colours: our own tones, chosen to stand out on light and
// dark backgrounds (TestProgramColoursWorkOnLightAndDark).
var (
	ProgramWarm  = color.NRGBA{0xdd, 0x65, 0x31, 0xff} // ember
	ProgramCool  = color.NRGBA{0x3e, 0x8f, 0xa8, 0xff} // slate teal
	ProgramLight = color.NRGBA{0xf7, 0xf6, 0xf3, 0xff} // the gauge on the disc
)

// ProgramIconSizes are the program ICO's entries, each drawn for its size:
// the sizes Explorer, the taskbar, Settings and notifications use at 100 %
// to 200 % display scale, and 256 for the largest views.
var ProgramIconSizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

// ProgramICO is the program icon file: Program at every ProgramIconSizes.
func ProgramICO() []byte {
	var imgs []*image.NRGBA
	for _, s := range ProgramIconSizes {
		imgs = append(imgs, Program(s))
	}
	return ICO(imgs...)
}

// Design draws a program icon on a transparent square of size pixels.
type Design func(size int) *image.NRGBA

// SplitFace is a disc whose left half is warm and right half cool, with a
// light scale arc over the top and a light needle from the hub pointing
// into the upper right. From 32 px a thin gap parts the halves (below
// that it would only blur them).
func SplitFace(size int) *image.NRGBA {
	c := float64(size) / 2
	ro := c - stroke(size, 0.03, 0.25)
	gap := stroke(size, 0.03, 0.9) / 2
	if size < 32 {
		gap = 0
	}
	at := stroke(size, 0.07, 1.3) // scale arc thickness
	ar := ro*0.70 + at/2          // its outer radius
	nw := stroke(size, 0.08, 1.5) // needle width
	hub := stroke(size, 0.09, 1.4)
	nx, ny := polar(c, ar-at/2, 40)
	return paint(size, func(x, y float64) color.NRGBA {
		dx, dy := x-c, y-c
		r := math.Hypot(dx, dy)
		if r > ro {
			return color.NRGBA{}
		}
		var out color.NRGBA
		switch {
		case dx < -gap:
			out = ProgramWarm
		case dx > gap:
			out = ProgramCool
		}
		if r <= ar && r >= ar-at && dy <= 0 {
			out = ProgramLight
		}
		if r <= hub || segDist(x, y, c, c, nx, ny) <= nw/2 {
			out = ProgramLight
		}
		return out
	})
}

// TwinBars (the runner-up) is a light rounded tile with two level bars
// side by side, the left warm and the right cool, each over its own track.
func TwinBars(size int) *image.NRGBA {
	s := float64(size)
	inset := stroke(size, 0.03, 0.25)
	rad := s * 0.2
	border := stroke(size, 0.035, 1)
	tile := ProgramLight
	edge := color.NRGBA{0x8a, 0x90, 0x99, 0xff}
	track := color.NRGBA{0xd9, 0xd5, 0xce, 0xff}
	barW := math.Round(s * 0.25)
	if size < 24 {
		barW = 4
	}
	gapX := math.Max(2, math.Round(s*0.10))
	x0 := math.Round((s - 2*barW - gapX) / 2)
	top, bottom := math.Round(s*0.2), s-math.Round(s*0.2)
	h := bottom - top
	levels := []float64{0.72, 0.45}
	cols := []color.NRGBA{ProgramWarm, ProgramCool}
	cr := barW / 4 // bar corner radius
	return paint(size, func(x, y float64) color.NRGBA {
		if roundRect(x, y, s/2, s/2, s/2-inset, s/2-inset, rad) > 0 {
			return color.NRGBA{}
		}
		out := tile
		if roundRect(x, y, s/2, s/2, s/2-inset, s/2-inset, rad) > -border {
			out = edge
		}
		for i := range 2 {
			bx := x0 + float64(i)*(barW+gapX)
			if roundRect(x, y, bx+barW/2, top+h/2, barW/2, h/2, cr) <= 0 {
				out = track
				if y >= bottom-h*levels[i] {
					out = cols[i]
				}
			}
		}
		return out
	})
}

// samples is the supersampling grid per pixel (samples×samples).
const samples = 8

// paint renders f, the colour (straight alpha) at a point in pixel
// coordinates, with samples×samples supersampling.
func paint(size int, f func(x, y float64) color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	n := float64(samples * samples)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					c := f(float64(px)+(float64(sx)+0.5)/samples, float64(py)+(float64(sy)+0.5)/samples)
					ca := float64(c.A) / 255
					r += float64(c.R) * ca
					g += float64(c.G) * ca
					b += float64(c.B) * ca
					a += ca
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(r / a)), G: uint8(math.Round(g / a)), B: uint8(math.Round(b / a)),
				A: uint8(math.Round(a / n * 255)),
			})
		}
	}
	return img
}

// segDist is the distance from (x, y) to the segment (x0, y0)-(x1, y1).
func segDist(x, y, x0, y0, x1, y1 float64) float64 {
	vx, vy := x1-x0, y1-y0
	t := ((x-x0)*vx + (y-y0)*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(x-(x0+t*vx), y-(y0+t*vy))
}

// roundRect is the signed distance from (x, y) to a rectangle centred on
// (cx, cy) with half sizes hw, hh and corner radius r (negative inside).
func roundRect(x, y, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(x-cx) - (hw - r)
	qy := math.Abs(y-cy) - (hh - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

// polar is the point at radius r and clockwise angle deg from 12 o'clock
// around (c, c).
func polar(c, r, deg float64) (float64, float64) {
	a := deg * math.Pi / 180
	return c + r*math.Sin(a), c - r*math.Cos(a)
}

// stroke is a width in pixels for size: frac of the size, at least min.
func stroke(size int, frac, min float64) float64 { return math.Max(min, float64(size)*frac) }
