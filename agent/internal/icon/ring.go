// Package icon draws the tray's ring gauge (with the percentage inside it
// on Windows and Linux), encodes PNG and ICO files, and rasterises the Clawd
// application icon. It uses only the standard library.
package icon

import (
	"image"
	"image/color"
	"math"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Colours (the GNOME extension's "Terracotta ring").
var (
	Normal   = color.NRGBA{0xd9, 0x77, 0x57, 0xff}
	Warning  = color.NRGBA{0xf5, 0xc2, 0x11, 0xff}
	Critical = color.NRGBA{0xf6, 0x61, 0x51, 0xff}
	None     = color.NRGBA{0x99, 0x99, 0x99, 0xff} // the "-" shown when there is no number
	Track    = color.NRGBA{0x80, 0x80, 0x80, 0x73}
	White    = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	Outline  = color.NRGBA{0x00, 0x00, 0x00, 0xa0}
)

// staleAlpha is the alpha of the filled part when the numbers are old.
const staleAlpha = 0x80

// Spec is what one icon shows.
type Spec struct {
	Percent *float64
	Level   usage.Level
	Stale   bool
	Number  bool // draw the rounded percentage in the middle
}

func levelColour(l usage.Level) (color.NRGBA, bool) {
	switch l {
	case usage.LevelNormal:
		return Normal, true
	case usage.LevelWarning:
		return Warning, true
	case usage.LevelCritical:
		return Critical, true
	}
	return color.NRGBA{}, false
}

// Ring draws the gauge on a transparent square of size pixels: the filled
// part runs clockwise from 12 o'clock.
func Ring(s Spec, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size) / 2
	outer := c - 0.5
	inner := outer - float64(max(2, int(math.Round(float64(size)*0.19))))
	fill, hasFill := levelColour(s.Level)
	if s.Stale {
		fill.A = staleAlpha
	}
	frac := usage.BarFraction(s.Percent)
	if s.Percent == nil {
		hasFill = false
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			r := math.Hypot(dx, dy)
			if r > outer || r < inner {
				continue
			}
			a := math.Atan2(dx, -dy) / (2 * math.Pi) // clockwise from 12 o'clock, 0..1
			if a < 0 {
				a++
			}
			if hasFill && a < frac {
				img.SetNRGBA(x, y, fill)
			} else {
				img.SetNRGBA(x, y, Track)
			}
		}
	}
	if s.Number {
		text, ink := "-", None
		if s.Percent != nil {
			n := int(math.Floor(*s.Percent + 0.5))
			n = min(max(n, 0), 999)
			text, ink = itoa(n), White
		}
		drawText(img, text, ink, max(1, size/16))
	}
	return img
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
