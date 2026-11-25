package icon

import (
	"image"
	"math"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// HeaderArt is the picture at the left of a provider's header in the
// drawn usage panel (drawn-panel design 5.1, D13) on a transparent square
// of size pixels: the provider's artwork scaled to the square's width and
// centred (edge pixels by the share of them covered, so any size is
// smooth), or for a provider without artwork a whole ring in the normal
// colour, as the GNOME panel draws it.
func HeaderArt(p Provider, size int) *image.NRGBA {
	a, ok := arts[p]
	if !ok {
		hundred := 100.0
		return Ring(Spec{Level: usage.LevelNormal, Percent: &hundred}, size)
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	w, h := a.size()
	f := math.Min(float64(size)/float64(w), float64(size)/float64(h)) // pixels per cell
	x0 := (float64(size) - f*float64(w)) / 2
	y0 := (float64(size) - f*float64(h)) / 2
	ink := func(cx, cy int) bool { return cx >= 0 && cy >= 0 && cx < w && cy < h && a.Rows[cy][cx] == '#' }
	// overlap of [lo, hi) with cell c of the grid starting at o
	overlap := func(lo, hi, o float64, c int) float64 {
		a, b := o+f*float64(c), o+f*float64(c+1)
		return math.Max(0, math.Min(hi, b)-math.Max(lo, a))
	}
	for y := range size {
		for x := range size {
			cover := 0.0
			fx, fy := float64(x), float64(y)
			for cy := int(math.Floor((fy - y0) / f)); float64(cy)*f+y0 < fy+1; cy++ {
				oy := overlap(fy, fy+1, y0, cy)
				for cx := int(math.Floor((fx - x0) / f)); float64(cx)*f+x0 < fx+1; cx++ {
					if ink(cx, cy) {
						cover += oy * overlap(fx, fx+1, x0, cx)
					}
				}
			}
			if cover <= 0 {
				continue
			}
			c := a.Ink
			c.A = uint8(math.Floor(float64(c.A)*math.Min(cover, 1) + 0.5))
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}
