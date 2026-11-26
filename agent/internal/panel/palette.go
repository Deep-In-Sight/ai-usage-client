package panel

import (
	"image/color"
	"math"
)

// Palette is the colours of the menu the panel is drawn in: its
// background, its text, and the highlight of the item under the pointer
// with that item's text. The dim text, the problem line's and the
// separator's colours follow from them.
//
// A background that is fully transparent is the menu's own, which the
// panel does not know (macOS: the menu's material shows through the
// items, design 6.3): nothing fills an item then, a faded colour is the
// colour at that opacity and Dark alone says which problem colour reads.
// Dimmed, when set, is the menu's own dim text colour (macOS'
// secondaryLabelColor) instead of the text mixed toward the background.
// HighlightInset and HighlightRadius (pixels at 96 DPI, scaled with the
// metrics; 0: the whole item, Windows) draw the highlight as a rounded
// fill inset from the item's sides, as macOS highlights its menu items.
type Palette struct {
	Dark                                       bool
	Background, Text, Highlight, HighlightText color.NRGBA
	Dimmed                                     color.NRGBA
	HighlightInset, HighlightRadius            int
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

// Problem is the problem line's colour: amber on a dark background, dark
// amber on a light one (design 5.2), judged by the background's luminance
// as the GNOME panel does, so that a high-contrast black menu (the light
// set in the system's colours) gets the amber.
func (p Palette) Problem() color.NRGBA {
	if p.Dark || (p.Background.A != 0 && luminance(p.Background) < 0.5) {
		return ProblemOnDark
	}
	return ProblemOnLight
}

// luminance is c's relative luminance (sRGB, linearised), as the GNOME
// extension's isDarkBackground computes it.
func luminance(c color.NRGBA) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// Separator is the colour of the line between the groups.
func (p Palette) Separator() color.NRGBA { return mix(p.Text, p.Background, SeparatorMix) }

// Fade is c drawn at opacity over background (a stale group, design 5.3);
// over the menu's own background (a transparent one, macOS) it is c at
// that opacity.
func Fade(c, background color.NRGBA, opacity float64) color.NRGBA {
	if background.A == 0 {
		c.A = uint8(math.Floor(float64(c.A)*opacity + 0.5))
		return c
	}
	return mix(c, background, 1-opacity)
}

// mix is a moved f of the way toward b, per channel, rounded.
func mix(a, b color.NRGBA, f float64) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(math.Floor(float64(x) + (float64(y)-float64(x))*f + 0.5)) }
	return color.NRGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), m(a.A, b.A)}
}
