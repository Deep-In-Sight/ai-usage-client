package panel

import (
	"image/color"
	"strings"
)

// Font is the menu font or a heavier face of it: bold for the provider's
// name and the percent (design 6.2), semi-bold for a limit's label (the
// approved mock).
type Font uint8

const (
	Regular Font = iota
	Bold
	SemiBold
)

// Align is where a text sits in its box.
type Align uint8

const (
	Left Align = iota
	Right
)

// OpKind is what an Op draws.
type OpKind uint8

const (
	Fill   OpKind = iota // a rectangle in Colour, its corners rounded by Radius
	Text                 // Text in Colour and Font, in one line, vertically centred in its box
	Pixels               // a premultiplied BGRA picture, blended at Opacity
)

// Op is one step of drawing an item, in pixels from the item's top left.
// The helper only carries them out (Windows: FillRect, DrawTextW,
// AlphaBlend), so where everything goes is decided here, the same on
// every OS and tested everywhere.
type Op struct {
	Kind       OpKind
	X, Y, W, H int
	Colour     color.NRGBA
	Font       Font
	Text       string
	Align      Align
	Ellipsis   bool   // a text too long for its box ends in "…"
	Pixels     []byte // W×H, as BarPixels gives them
	Opacity    uint8  // Pixels: 255, a stale group's 153
	Radius     int    // Fill: the corners' radius (0: square)
}

// StaleAlpha is StaleOpacity as a picture's constant alpha (0.6 × 255,
// rounded).
const StaleAlpha uint8 = 153

// Measurer measures text in the menu font at the metrics' DPI.
type Measurer interface {
	TextWidth(f Font, s string) int
	LineHeight(f Font) int
}

// Size is an item's size: what Windows is told when it measures the item,
// and the height Layout fills. The width is the panel's, or more when a
// header's or an action's text needs it (Windows widens the menu to its
// widest item: nothing is cut, design 2); a line that cannot be clicked
// (the server line, the sign-in prompt) wraps at the panel's text width
// instead, as a header's problem line does, so the panel keeps its width
// in the usual team case. The height grows with the menu font and the
// wrapped lines, never cutting a line.
func Size(it Item, m Metrics, tm Measurer) (w, h int) {
	line := tm.LineHeight(Regular)
	switch it.Kind {
	case "header":
		h = headerLine(m, tm)
		if lines := Wrap(it.Problem, Regular, m.Width-2*m.Pad, tm); len(lines) > 0 {
			h += problemHeight(len(lines), m, line)
		}
		return max(m.Width, headerWidth(it, m, tm)), h
	case "row":
		return m.Width, max(m.Row, rowBlock(m, tm)+2*m.px(4))
	case "action":
		return max(m.Width, m.Pad+m.Check+tm.TextWidth(Regular, it.Text)+m.Pad), textHeight(m, line)
	case "info":
		w = m.Width
		lines := Wrap(it.Text, Regular, m.Width-2*m.Pad, tm)
		for _, l := range lines { // a word longer than a line stays whole
			w = max(w, m.Pad+tm.TextWidth(Regular, l)+m.Pad)
		}
		return w, textHeight(m, line) + max(len(lines)-1, 0)*(line+m.px(2))
	}
	return m.Width, textHeight(m, line) // "more" and anything unknown
}

// headerWidth is what a header's line needs: the picture, the name, the
// account and the time, each 8 px apart, between the pads.
func headerWidth(it Item, m Metrics, tm Measurer) int {
	return m.Pad + m.Picture + m.px(8) + tm.TextWidth(Bold, it.Name) + m.px(8) + tm.TextWidth(Regular, it.Account) +
		m.px(8) + tm.TextWidth(Regular, it.Updated) + m.Pad
}

// SeparatorSize is a separator's size (it adds no width).
func SeparatorSize(m Metrics) (w, h int) { return 0, m.Separator }

func headerLine(m Metrics, tm Measurer) int {
	return max(m.Header, tm.LineHeight(Bold)+m.px(12), m.Picture+m.px(8))
}

func textHeight(m Metrics, line int) int { return max(m.Item, line+m.px(12)) }

// problemHeight: the first line in m.Problem, each further one a line and
// 2 px more.
func problemHeight(n int, m Metrics, line int) int {
	return max(m.Problem, line+m.px(12)) + (n-1)*(line+m.px(2))
}

// rowBlock is the height of a row's label, bar and reset line together.
func rowBlock(m Metrics, tm Measurer) int {
	return rowFirst(tm) + m.px(7) + m.Bar + m.px(6) + tm.LineHeight(Regular)
}

// rowFirst is the height of a row's first line (its label and percent).
func rowFirst(tm Measurer) int { return max(tm.LineHeight(SemiBold), tm.LineHeight(Bold)) }

// Layout is how to draw it in a w×h item (h as Size gave, w at least its
// width) with pal; selected: the item is under the pointer (or chosen
// with the keyboard), which highlights only an item that can be clicked,
// and then every text of it is in the highlight's text colour (the bars
// keep theirs).
func Layout(it Item, m Metrics, tm Measurer, w, h int, pal Palette, selected bool) []Op {
	bg, text := pal.Background, pal.Text
	clickable := it.Kind == "row" || it.Kind == "more" || (it.Kind == "action" && it.Enabled)
	highlighted := selected && clickable
	if highlighted {
		bg, text = pal.Highlight, pal.HighlightText
	}
	dim := Dim(text, bg)
	switch {
	case highlighted:
		dim = text
	case pal.Dimmed.A != 0:
		dim = pal.Dimmed
	}
	opacity := uint8(255)
	if it.Stale {
		// the group at 0.6 (design 5.3), its dim text left as it is: dim
		// already, not faded twice (GNOME's rule, view.js textOpacity;
		// review A-m3); under the pointer the dim text is the faded text
		text = Fade(text, bg, StaleOpacity)
		if highlighted {
			dim = text
		}
		opacity = StaleAlpha
	}
	ops := []Op{{Kind: Fill, W: w, H: h, Colour: bg}}
	if highlighted && pal.HighlightInset > 0 {
		in := m.px(pal.HighlightInset)
		ops = []Op{{Kind: Fill, W: w, H: h, Colour: pal.Background},
			{Kind: Fill, X: in, W: max(w-2*in, 0), H: h, Colour: bg, Radius: m.px(pal.HighlightRadius)}}
	}
	put := func(o Op) {
		// nothing leaves the item, whatever the texts and the font
		o.X, o.Y = min(max(o.X, 0), w), min(max(o.Y, 0), h)
		o.W, o.H = min(max(o.W, 0), w-o.X), min(max(o.H, 0), h-o.Y)
		if o.W > 0 && o.H > 0 {
			ops = append(ops, o)
		}
	}
	text1 := func(x, y, width, height int, s string, f Font, c color.NRGBA, a Align, ellipsis bool) {
		if s != "" {
			put(Op{Kind: Text, X: x, Y: y, W: width, H: height, Text: s, Font: f, Colour: c, Align: a, Ellipsis: ellipsis})
		}
	}
	right := w - m.Pad // the content's right edge
	line := tm.LineHeight(Regular)
	switch it.Kind {
	case "header":
		hl := headerLine(m, tm)
		pic := m.Picture
		if px := HeaderPicture(it.Provider, pic); len(px) > 0 && pic <= hl && m.Pad+pic <= w {
			ops = append(ops, Op{Kind: Pixels, X: m.Pad, Y: (hl - pic) / 2, W: pic, H: pic, Pixels: px, Opacity: opacity})
		}
		x := m.Pad + pic + m.px(8)
		nw := min(tm.TextWidth(Bold, it.Name), max(right-x, 0))
		text1(x, 0, nw, hl, it.Name, Bold, text, Left, true)
		x += nw + m.px(8)
		uw := 0
		if it.Updated != "" {
			uw = tm.TextWidth(Regular, it.Updated)
			text1(right-uw, 0, uw, hl, it.Updated, Regular, dim, Right, false)
		}
		end := right
		if uw > 0 {
			end = right - uw - m.px(8)
		}
		text1(x, 0, end-x, hl, it.Account, Regular, dim, Left, true)
		y := hl + (max(m.Problem, line+m.px(12))-line)/2
		for _, l := range Wrap(it.Problem, Regular, m.Width-2*m.Pad, tm) {
			text1(m.Pad, y, right-m.Pad, line, l, Regular, problemColour(pal, bg, it.Stale), Left, true)
			y += line + m.px(2)
		}
	case "row":
		top := (h - rowBlock(m, tm)) / 2
		first := rowFirst(tm)
		pw := 0
		if it.PercentText != "" {
			pw = tm.TextWidth(Bold, it.PercentText)
			c := text
			if it.Level == "critical" && !highlighted {
				c = Critical
				if it.Stale {
					c = Fade(c, bg, StaleOpacity)
				}
			}
			text1(right-pw, top, pw, first, it.PercentText, Bold, c, Right, false)
		}
		lw := right - m.Pad
		if pw > 0 {
			lw -= pw + m.px(8)
		}
		text1(m.Pad, top, lw, first, it.Label, SemiBold, text, Left, true)
		by := top + first + m.px(7)
		track := right - m.Pad
		fill := 0
		bar, ok := BarColour(it.Level)
		if ok {
			fill = FillWidth(it.Percent, track, m.Bar)
		} else {
			bar = Normal
		}
		if track > 0 {
			put(Op{Kind: Pixels, X: m.Pad, Y: by, W: track, H: m.Bar, Pixels: BarPixels(track, m.Bar, fill, bar, Track), Opacity: opacity})
		}
		text1(m.Pad, by+m.Bar+m.px(6), right-m.Pad, line, it.Detail, Regular, dim, Left, true)
	case "action":
		c := text
		if !it.Enabled {
			c = dim
		}
		if it.Checkbox && it.Checked {
			cs := line * 3 / 4
			put(Op{Kind: Pixels, X: m.Pad + (m.Check-cs)/2, Y: (h - cs) / 2, W: cs, H: cs, Pixels: CheckPixels(cs, c), Opacity: 255})
		}
		text1(m.Pad+m.Check, 0, right-m.Pad-m.Check, h, it.Text, Regular, c, Left, true)
	case "info":
		c := text
		if it.Dim {
			c = dim
		}
		lines := Wrap(it.Text, Regular, m.Width-2*m.Pad, tm)
		y := (h - len(lines)*line - max(len(lines)-1, 0)*m.px(2)) / 2
		for _, l := range lines {
			text1(m.Pad, y, right-m.Pad, line, l, Regular, c, Left, true)
			y += line + m.px(2)
		}
	case "more":
		text1(m.Pad, 0, right-m.Pad, h, it.Label, Regular, text, Left, true)
	}
	return ops
}

func problemColour(pal Palette, bg color.NRGBA, stale bool) color.NRGBA {
	if stale {
		return Fade(pal.Problem(), bg, StaleOpacity)
	}
	return pal.Problem()
}

// SeparatorOps draws a separator: a line across the content, in the
// middle.
func SeparatorOps(m Metrics, w, h int, pal Palette) []Op {
	ops := []Op{{Kind: Fill, W: w, H: h, Colour: pal.Background}}
	if lw := w - 2*m.Pad; lw > 0 && h > 0 {
		ops = append(ops, Op{Kind: Fill, X: m.Pad, Y: h / 2, W: lw, H: max(1, m.px(1)), Colour: pal.Separator()})
	}
	return ops
}

// Wrap breaks s into lines of at most width pixels at its spaces; a word
// wider than a line stays whole on a line of its own.
func Wrap(s string, f Font, width int, tm Measurer) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		if cur == "" {
			cur = word
			continue
		}
		if next := cur + " " + word; tm.TextWidth(f, next) <= width {
			cur = next
			continue
		}
		lines = append(lines, cur)
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
