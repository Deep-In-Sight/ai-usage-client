// Package panel is what the Windows and macOS helpers need to draw the
// usage panel (drawn-panel design 5.2, 6.2, 6.3) that is not OS code: the
// colours, the bar's fill width and its anti-aliased picture, the drawing
// data of each menu item and the sizes at a given DPI. Pure Go.
package panel

import (
	"image/color"
	"math"
)

// The original extension's colours (design 5.2), the same for both
// providers.
var (
	Normal   = color.NRGBA{0xd9, 0x77, 0x57, 0xff}
	Warning  = color.NRGBA{0xf5, 0xc2, 0x11, 0xff}
	Critical = color.NRGBA{0xf6, 0x61, 0x51, 0xff}
	Track    = color.NRGBA{0x80, 0x80, 0x80, 82} // rgba(128,128,128,0.32): 0.32 × 255, rounded

	// The problem line: amber on a dark menu, a dark amber on a light
	// one (#f5c211 is about 1.6:1 on a light menu, design 5.2).
	ProblemOnDark  = color.NRGBA{0xf5, 0xc2, 0x11, 0xff}
	ProblemOnLight = color.NRGBA{0x9a, 0x67, 0x00, 0xff}
)

const (
	StaleOpacity = 0.6  // a stale group is drawn at this opacity
	DimMix       = 0.45 // dim text: the menu's text colour mixed this far toward its background
	BarHeight    = 8    // the bar's height at 96 DPI (MetricsAt scales it)
)

// BarColour is the bar's colour for a level (usage.Level); false for none
// (no value: an empty track).
func BarColour(level string) (color.NRGBA, bool) {
	switch level {
	case "normal":
		return Normal, true
	case "warning":
		return Warning, true
	case "critical":
		return Critical, true
	}
	return color.NRGBA{}, false
}

// FillWidth is how much of a track of width track the bar fills for the
// shown percent (plan PR-18, the original extension's rule): none for no
// value or 0 %, the whole track from 100 %, otherwise the share of the
// track but at least a round dot (barHeight). percent is rounded first,
// as the number next to the bar is.
func FillWidth(percent *float64, track, barHeight int) int {
	if percent == nil || track <= 0 {
		return 0
	}
	p := math.Floor(*percent + 0.5)
	switch {
	case p <= 0:
		return 0
	case p >= 100:
		return track
	}
	w := int(math.Floor(float64(track)*p/100 + 0.5))
	return min(max(w, barHeight), track)
}

// Dim is text mixed DimMix toward background: the dim lines (account,
// "Updated", reset lines, the server line).
func Dim(text, background color.NRGBA) color.NRGBA {
	mix := func(a, b uint8) uint8 {
		return uint8(math.Floor(float64(a) + (float64(b)-float64(a))*DimMix + 0.5))
	}
	return color.NRGBA{mix(text.R, background.R), mix(text.G, background.G), mix(text.B, background.B), mix(text.A, background.A)}
}
