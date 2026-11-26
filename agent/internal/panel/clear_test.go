package panel

import (
	"image/color"
	"testing"
)

// macPalette is a palette as the macOS view gets it: no background of its
// own (the menu's material shows through), the system's label and
// secondary label colours, the selection's colours and its inset, rounded
// highlight (drawn-panel design 6.3).
func macPalette(dark bool) Palette {
	p := Palette{Dark: dark, Text: color.NRGBA{0, 0, 0, 0xd8}, Dimmed: color.NRGBA{0, 0, 0, 0x80},
		Highlight: color.NRGBA{0x00, 0x64, 0xe1, 0xff}, HighlightText: color.NRGBA{0xff, 0xff, 0xff, 0xff},
		HighlightInset: 5, HighlightRadius: 4}
	if dark {
		p.Text, p.Dimmed = color.NRGBA{0xff, 0xff, 0xff, 0xd8}, color.NRGBA{0xff, 0xff, 0xff, 0x8c}
	}
	return p
}

// Plan Task 9, F11: over a menu whose background is not the panel's own
// (macOS), nothing fills the item, the dim lines take the palette's own
// dim colour, a stale group is its colours at 0.6 of their opacity, and the
// problem line's colour follows the appearance.
func TestLayoutOverTheMenusOwnBackground(t *testing.T) {
	m := MetricsAt(96)
	for _, dark := range []bool{false, true} {
		pal := macPalette(dark)
		ops := Layout(row("normal", 42), m, tm96, 320, 64, pal, false)
		eq(t, ops[0].Kind, Fill)
		eq(t, ops[0].Colour.A, uint8(0))
		for _, o := range ops[1:] {
			if o.Kind == Fill {
				t.Fatalf("dark %v: a fill over the menu's background: %+v", dark, o)
			}
		}
		eq(t, find(t, ops, Text, "5-hour").Colour, pal.Text)
		eq(t, find(t, ops, Text, "42%").Colour, pal.Text)
		eq(t, find(t, ops, Text, "Resets Fri 21:20 · in 2h 20m").Colour, pal.Dimmed)

		st := row("critical", 95)
		st.Stale = true
		ops = Layout(st, m, tm96, 320, 64, pal, false)
		at := func(c color.NRGBA) color.NRGBA { c.A = uint8(float64(c.A)*StaleOpacity + 0.5); return c }
		eq(t, find(t, ops, Text, "5-hour").Colour, at(pal.Text))
		eq(t, find(t, ops, Text, "Resets Fri 21:20 · in 2h 20m").Colour, at(pal.Dimmed))
		eq(t, find(t, ops, Text, "42%").Colour, at(Critical))
		eq(t, find(t, ops, Pixels, "").Opacity, StaleAlpha)

		want := ProblemOnLight
		if dark {
			want = ProblemOnDark
		}
		eq(t, pal.Problem(), want)
		h := header()
		h.Problem = "Sign-in expired"
		eq(t, find(t, Layout(h, m, tm96, 320, 60, pal, false), Text, h.Problem).Colour, want)
		h.Stale = true
		eq(t, find(t, Layout(h, m, tm96, 320, 60, pal, false), Text, h.Problem).Colour, at(want))
		eq(t, find(t, Layout(h, m, tm96, 320, 60, pal, false), Text, "Updated 18:52").Colour, at(pal.Dimmed))

		inf := Item{Kind: "info", Text: "Server connected", Dim: true}
		eq(t, find(t, Layout(inf, m, tm96, 320, 30, pal, false), Text, "Server connected").Colour, pal.Dimmed)
	}
	// Fade over no background is the colour at that opacity
	eq(t, Fade(Normal, color.NRGBA{}, StaleOpacity), color.NRGBA{0xd9, 0x77, 0x57, 153})
}

// The macOS selection: a rounded fill inset from the item's sides in the
// selection colour, every text in the selection's text colour; an item
// that cannot be clicked is never highlighted; Windows' palettes (no
// inset) still fill the whole item.
func TestLayoutInsetHighlight(t *testing.T) {
	for _, dpi := range []int{96, 192} {
		m := MetricsAt(dpi)
		s := func(v int) int { return v * dpi / 96 }
		pal := macPalette(false)
		w, h := s(320), s(64)
		ops := Layout(row("normal", 42), m, fixedMeasurer{dpi}, w, h, pal, true)
		eq(t, ops[0].Colour.A, uint8(0))
		eq(t, ops[1], Op{Kind: Fill, X: s(5), W: w - 2*s(5), H: h, Colour: pal.Highlight, Radius: s(4)})
		eq(t, find(t, ops, Text, "5-hour").Colour, pal.HighlightText)
		eq(t, find(t, ops, Text, "Resets Fri 21:20 · in 2h 20m").Colour, pal.HighlightText)

		hd := Layout(header(), m, fixedMeasurer{dpi}, w, s(32), pal, true)
		for _, o := range hd {
			if o.Kind == Fill && o.Colour.A != 0 {
				t.Fatalf("a header is highlighted: %+v", o)
			}
		}
	}
	ops := Layout(row("normal", 42), MetricsAt(96), tm96, 350, 64, LightPalette, true)
	eq(t, ops[0], Op{Kind: Fill, W: 350, H: 64, Colour: LightPalette.Highlight})
	for _, o := range ops {
		if o.Radius != 0 {
			t.Fatalf("Windows got a rounded step: %+v", o)
		}
	}
}

// What VoiceOver says for a drawn item (design 6.3; the plan's
// "accessibility stays the OS's"): its texts in reading order.
func TestAccessibleText(t *testing.T) {
	h := header()
	eq(t, AccessibleText(h), "Claude, team-a@… · 3 PCs, Updated 18:52")
	h.Problem = "Sign-in expired: open Claude Code to renew it"
	h.Account = ""
	eq(t, AccessibleText(h), "Claude, Updated 18:52, Sign-in expired: open Claude Code to renew it")
	eq(t, AccessibleText(row("normal", 42)), "5-hour, 42%, Resets Fri 21:20 · in 2h 20m")
	eq(t, AccessibleText(Item{Kind: "more", Label: "More limits on the dashboard"}), "More limits on the dashboard")
	eq(t, AccessibleText(Item{Kind: "info", Text: "Server connected"}), "Server connected")
	eq(t, AccessibleText(Item{Kind: "action", Text: "Quit"}), "Quit")
}
