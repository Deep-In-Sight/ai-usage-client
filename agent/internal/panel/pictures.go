package panel

import (
	"image"
	"image/color"
	"math"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
)

// HeaderPicture is the picture at the left of a provider's header, size
// pixels square, as BarPixels gives its picture (premultiplied blue,
// green, red, alpha, the top row first): Clawd for Claude, the ring for
// Codex (icon.HeaderArt).
func HeaderPicture(provider string, size int) []byte {
	if size <= 0 {
		return []byte{}
	}
	return premultiplied(icon.HeaderArt(icon.Provider(provider), size))
}

// CheckPixels is a check mark size pixels square in c, as BarPixels gives
// its picture: two strokes, their edges by the share of each pixel
// covered.
func CheckPixels(size int, c color.NRGBA) []byte {
	if size <= 0 {
		return []byte{}
	}
	s := float64(size)
	// the stroke's middle line, in pixels: down to the corner, then up
	x0, y0, x1, y1, x2, y2 := 0.12*s, 0.52*s, 0.40*s, 0.80*s, 0.88*s, 0.22*s
	half := math.Max(0.75, s/14) // half the stroke's width
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			n := 0
			for j := range sub {
				for i := range sub {
					px := float64(x) + (float64(i)+0.5)/sub
					py := float64(y) + (float64(j)+0.5)/sub
					if segDist(px, py, x0, y0, x1, y1) <= half || segDist(px, py, x1, y1, x2, y2) <= half {
						n++
					}
				}
			}
			if n > 0 {
				ink := c
				ink.A = uint8(math.Floor(float64(c.A)*float64(n)/(sub*sub) + 0.5))
				img.SetNRGBA(x, y, ink)
			}
		}
	}
	return premultiplied(img)
}

// segDist is the distance from (x, y) to the segment (x0, y0)-(x1, y1).
func segDist(x, y, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	t := math.Max(0, math.Min(1, ((x-x0)*dx+(y-y0)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(x-(x0+t*dx), y-(y0+t*dy))
}

// premultiplied is img as premultiplied BGRA bytes, the top row first.
func premultiplied(img *image.NRGBA) []byte {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			pm := func(v uint8) byte { return byte(math.Floor(float64(v)*float64(c.A)/255 + 0.5)) }
			out = append(out, pm(c.B), pm(c.G), pm(c.R), c.A)
		}
	}
	return out
}
