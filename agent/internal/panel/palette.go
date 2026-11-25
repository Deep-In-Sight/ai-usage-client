package panel

import (
	"image/color"
	"math"
)

// Palette is the colours of the menu the panel is drawn in: its
// background, its text, and the highlight of the item under the pointer
// with that item's text. The dim text, the problem line's and the
// separator's colours follow from them.
type Palette struct {
	Dark                                       bool
	Background, Text, Highlight, HighlightText color.NRGBA
}

var (
	// LightPalette is Windows' light menu as it draws a menu that holds
	// an owner-drawn item (the classic style, stage 0): COLOR_MENU,
	// COLOR_MENUTEXT, COLOR_HIGHLIGHT, COLOR_HIGHLIGHTTEXT at their
	// defaults. The Windows helper reads the system's own values.
	LightPalette = Palette{Background: color.NRGBA{0xf0, 0xf0, 0xf0, 0xff}, Text: color.NRGBA{0, 0, 0, 0xff},
		Highlight: color.NRGBA{0x00, 0x78, 0xd4, 0xff}, HighlightText: color.NRGBA{0xff, 0xff, 0xff, 0xff}}
	// DarkPalette is Windows' dark menu as stage 0 measured it (#2b2b2b,
	// white text, the hovered item #414141).
	DarkPalette = Palette{Dark: true, Background: color.NRGBA{0x2b, 0x2b, 0x2b, 0xff}, Text: color.NRGBA{0xff, 0xff, 0xff, 0xff},
		Highlight: color.NRGBA{0x41, 0x41, 0x41, 0xff}, HighlightText: color.NRGBA{0xff, 0xff, 0xff, 0xff}}
)

// SeparatorMix is how far the separator's line is mixed from the text
// toward the background.
const SeparatorMix = 0.8

// Problem is the problem line's colour: amber on a dark menu, dark amber
// on a light one (design 5.2).
func (p Palette) Problem() color.NRGBA {
	if p.Dark {
		return ProblemOnDark
	}
	return ProblemOnLight
}

// Separator is the colour of the line between the groups.
func (p Palette) Separator() color.NRGBA { return mix(p.Text, p.Background, SeparatorMix) }

// Fade is c drawn at opacity over background (a stale group, design 5.3).
func Fade(c, background color.NRGBA, opacity float64) color.NRGBA {
	return mix(c, background, 1-opacity)
}

// mix is a moved f of the way toward b, per channel, rounded.
func mix(a, b color.NRGBA, f float64) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(math.Floor(float64(x) + (float64(y)-float64(x))*f + 0.5)) }
	return color.NRGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), m(a.A, b.A)}
}
