package panel

import (
	"image/color"
	"math"
)

// sub is the number of sub-samples per pixel side: a pixel's coverage is
// counted over sub × sub points.
const sub = 4

// covered counts the sub-samples of pixel (x, y) that lie inside a capsule
// (a bar with fully rounded ends) from 0 to length across and h high: its
// ends are half circles of radius min(length, h)/2. The arithmetic is in
// integers (eighths of a pixel, the sub-samples at odd eighths), so the
// top and bottom rows, and the two ends, come out exactly alike.
func covered(x, y, length, h int) int {
	// in eighths of a pixel: the radius, the middle row and the centres of
	// the two end circles
	const e = 2 * sub
	r := e / 2 * min(length, h)
	cy := e / 2 * h
	left := r
	right := e*length - r
	n := 0
	for j := range sub {
		dy := e*y + 2*j + 1 - cy
		for i := range sub {
			sx := e*x + 2*i + 1
			var dx int
			switch {
			case sx < left:
				dx = sx - left
			case sx > right:
				dx = sx - right
			}
			if dx*dx+dy*dy <= r*r {
				n++
			}
		}
	}
	return n
}

// BarPixels is the picture of a bar w × h pixels whose first fill pixels
// are filled with bar and the rest is track: 4 bytes per pixel,
// premultiplied blue, green, red, alpha, the top row first (a Windows
// AlphaBlend source; macOS and the tests read it the same way). Both the
// track and the fill have fully rounded ends (radius h/2); an edge pixel's
// alpha is the share of it covered.
func BarPixels(w, h, fill int, bar, track color.NRGBA) []byte {
	out := make([]byte, w*h*4)
	fill = min(max(fill, 0), w)
	const all = sub * sub
	for y := range h {
		for x := range w {
			af := 0.0 // the fill's alpha over this pixel
			if fill > 0 {
				af = float64(covered(x, y, fill, h)) / all * float64(bar.A) / 255
			}
			at := float64(covered(x, y, w, h)) / all * float64(track.A) / 255 * (1 - af) // the track's, under the fill
			ch := func(b, t uint8) byte { return byte(math.Floor(float64(b)*af + float64(t)*at + 0.5)) }
			i := (y*w + x) * 4
			out[i] = ch(bar.B, track.B)
			out[i+1] = ch(bar.G, track.G)
			out[i+2] = ch(bar.R, track.R)
			out[i+3] = byte(math.Floor(255*(af+at) + 0.5))
		}
	}
	return out
}
