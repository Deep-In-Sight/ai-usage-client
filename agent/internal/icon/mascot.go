package icon

import (
	"image"
	"image/color"
	"math"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Provider is whose account the tray icon stands for. Its artwork is drawn
// with the gauge; "" (no account detected) or a provider without artwork
// gets the neutral ring.
type Provider string

// Claude is a Claude Code sign-in.
const Claude Provider = "claude"

// Art is a provider's artwork as a pixel grid ('#' is ink), drawn only at
// whole multiples so it stays crisp. It must fit 14×8.
type Art struct {
	Rows []string
	Ink  color.NRGBA
}

// arts is the artwork per provider. A second provider is one more entry.
var arts = map[Provider]Art{
	// Clawd, the Claude Code mascot ((c) Anthropic PBC, see README): the
	// GNOME extension's clawd.svg at its own 11×8 grid (TestClawdArtIsTheSVGGrid).
	Claude: {Rows: []string{
		".#########.",
		".#########.",
		"##.#####.##",
		"###########",
		".#########.",
		".#########.",
		".#.#...#.#.",
		".#.#...#.#.",
	}, Ink: color.NRGBA{0xd9, 0x77, 0x57, 0xff}},
}

// Tray draws the tray icon on a transparent square of size pixels. With
// the provider's artwork it is the artwork over either the number in the
// level colour (s.Number: Windows, Linux desktops without the label) or a
// bar gauge (macOS and GNOME, which show the number next to the icon).
// Old numbers fade the bar's fill, or with the number the artwork.
// Without artwork it is Ring.
func Tray(s Spec, size int) *image.NRGBA {
	a, ok := arts[s.Provider]
	if !ok {
		return Ring(s, size)
	}
	if s.Number {
		return withNumber(a, s, size)
	}
	return withBar(a, s, size)
}

func (a Art) size() (w, h int) { return len(a.Rows[0]), len(a.Rows) }

func (a Art) draw(img *image.NRGBA, x0, y0, m int) {
	for y, row := range a.Rows {
		for x := range row {
			if row[x] != '#' {
				continue
			}
			for sy := 0; sy < m; sy++ {
				for sx := 0; sx < m; sx++ {
					img.SetNRGBA(x0+x*m+sx, y0+y*m+sy, a.Ink)
				}
			}
		}
	}
}

// fillColour is the level colour, faded when the numbers are old; ok is
// false when there is no number to show.
func fillColour(s Spec) (color.NRGBA, bool) {
	c, ok := levelColour(s.Level)
	if s.Stale {
		c.A = staleAlpha
	}
	return c, ok && s.Percent != nil
}

// centred is where n cells of m pixels start when centred in size pixels,
// on whole cells: an icon at twice the size is the smaller one at m=2,
// so a host that halves it gets the small one back unblurred.
func centred(size, n, m int) int { return (size/m - n) / 2 * m }

// withBar: on a 16 px grid (times m = size/16) the artwork, a 2 px gap
// and a 2 px bar as wide as the artwork, centred together.
func withBar(a Art, s Spec, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	m := max(1, size/16)
	aw, ah := a.size()
	w, h := aw*m, ah*m
	gap, bar := 2*m, 2*m
	x0, y0 := centred(size, aw, m), centred(size, ah+4, m) // 4: gap and bar on the 16 px grid
	a.draw(img, x0, y0, m)
	fill, hasFill := fillColour(s)
	frac := usage.BarFraction(s.Percent)
	for x := 0; x < w; x++ {
		c := Track
		// filled by whole cells (one column per artwork column)
		if hasFill && (float64(x/m)+0.5)/float64(aw) < frac {
			c = fill
		}
		for y := y0 + h + gap; y < y0+h+gap+bar; y++ {
			img.SetNRGBA(x0+x, y, c)
		}
	}
	return img
}

// numberText is the rounded percentage, or "-" without one.
func numberText(s Spec) string {
	if s.Percent == nil {
		return "-"
	}
	n := int(math.Floor(*s.Percent + 0.5))
	return itoa(min(max(n, 0), 999))
}

// numberLayout picks, for a size and a number of characters, the
// artwork scale m (size/16, the same for every number), the digit scale d
// (the largest whose outlined block fits the width and, under the
// artwork, the height) and the blank rows between the two (1, or 0 when
// that buys larger digits).
func numberLayout(a Art, size, chars int) (m, d, gap int) {
	_, h := a.size()
	m = max(1, size/16)
	for d = size / 5; d > 1; d-- {
		if (4*chars-1)*d+2 > size {
			continue
		}
		for gap = 1; gap >= 0; gap-- {
			if h*m+gap+5*d+2 <= size {
				return m, d, gap
			}
		}
	}
	return m, 1, 1
}

// withNumber: the artwork at the top, the outlined number under it.
func withNumber(a Art, s Spec, size int) *image.NRGBA {
	text := numberText(s)
	m, d, gap := numberLayout(a, size, len(text))
	return numberAt(a, s, size, text, m, d, gap)
}

func numberAt(a Art, s Spec, size int, text string, m, d, gap int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	w, h := a.size()
	w, h = w*m, h*m
	block := h + gap + 1 + 5*d + 1 // the digits' outline is 1 px
	top := max(0, (size-block)/2)
	// Old numbers fade the artwork, as they faded the ring; the digits keep
	// their full colour so they stay readable (faded digits are dark on a
	// dark taskbar).
	if s.Stale {
		a.Ink.A = staleAlpha
	}
	a.draw(img, centred(size, w/m, m), top, m)
	ink, ok := levelColour(s.Level)
	if !ok || s.Percent == nil {
		ink = None
	}
	tw := (4*len(text) - 1) * d
	drawTextAt(img, text, ink, d, (size-tw)/2, top+h+gap+1)
	return img
}
