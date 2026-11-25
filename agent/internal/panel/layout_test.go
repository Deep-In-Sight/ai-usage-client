package panel

import (
	"bytes"
	"image/color"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
)

// fixedMeasurer measures 7 px per character (bold 8) and 16 px lines at
// 96 DPI, scaled for others: a stand-in for the menu font.
type fixedMeasurer struct{ dpi int }

func (f fixedMeasurer) s(v int) int { return (v*f.dpi + 48) / 96 }
func (f fixedMeasurer) TextWidth(font Font, s string) int {
	per := 7
	if font == Bold {
		per = 8
	}
	return f.s(per * utf8.RuneCountInString(s))
}
func (f fixedMeasurer) LineHeight(Font) int { return f.s(16) }

var tm96 = fixedMeasurer{96}

func header() Item {
	return Item{Kind: "header", Provider: "claude", Name: "Claude", Account: "team-a@… · 3 PCs", Updated: "Updated 18:52"}
}

func row(level string, p float64) Item {
	return Item{Kind: "row", Provider: "claude", Label: "5-hour", PercentText: "42%", Detail: "Resets Fri 21:20 · in 2h 20m", Level: level, Percent: &p}
}

func find(t *testing.T, ops []Op, kind OpKind, text string) Op {
	t.Helper()
	for _, o := range ops {
		if o.Kind == kind && (kind != Text || o.Text == text) {
			return o
		}
	}
	t.Fatalf("no %v op %q in %+v", kind, text, ops)
	return Op{}
}

func TestPalettes(t *testing.T) {
	eq(t, LightPalette, Palette{Background: color.NRGBA{0xf0, 0xf0, 0xf0, 0xff}, Text: color.NRGBA{0, 0, 0, 0xff},
		Highlight: color.NRGBA{0x00, 0x78, 0xd4, 0xff}, HighlightText: color.NRGBA{0xff, 0xff, 0xff, 0xff}})
	eq(t, DarkPalette, Palette{Dark: true, Background: color.NRGBA{0x2b, 0x2b, 0x2b, 0xff}, Text: color.NRGBA{0xff, 0xff, 0xff, 0xff},
		Highlight: color.NRGBA{0x41, 0x41, 0x41, 0xff}, HighlightText: color.NRGBA{0xff, 0xff, 0xff, 0xff}})
	eq(t, LightPalette.Problem(), ProblemOnLight)
	eq(t, DarkPalette.Problem(), ProblemOnDark)
	eq(t, LightPalette.Separator(), color.NRGBA{0xc0, 0xc0, 0xc0, 0xff})
	eq(t, DarkPalette.Separator(), color.NRGBA{0x55, 0x55, 0x55, 0xff})
	// opacity 0.6 over the background
	eq(t, Fade(Normal, DarkPalette.Background, StaleOpacity), color.NRGBA{0x93, 0x59, 0x45, 0xff})
}

func TestSizes(t *testing.T) {
	m := MetricsAt(96)
	for _, c := range []struct {
		name string
		it   Item
		w, h int
	}{
		{"header", header(), 320, 32},
		{"header and problem", Item{Kind: "header", Name: "Codex", Problem: "Sign-in expired: open Codex to renew it"}, 320, 32 + 28},
		{"header and a long problem", Item{Kind: "header", Name: "Codex", Problem: strings.Repeat("word ", 12)}, 320, 32 + 28 + 18},
		{"row", row("normal", 42), 320, 64},
		{"more", Item{Kind: "more", Label: "More limits on the dashboard"}, 320, 30},
		{"action", Item{Kind: "action", Text: "Refresh now", Enabled: true}, 320, 30},
		{"info", Item{Kind: "info", Text: "Server connected", Dim: true}, 320, 30},
		// a long line widens the menu (nothing is cut): pad + text + pad
		{"long info", Item{Kind: "info", Text: strings.Repeat("x", 50), Dim: true}, 16 + 350 + 16, 30},
		{"long action", Item{Kind: "action", Text: strings.Repeat("x", 50)}, 16 + 22 + 350 + 16, 30},
	} {
		w, h := Size(c.it, m, tm96)
		if w != c.w || h != c.h {
			t.Fatalf("%s: %dx%d, want %dx%d", c.name, w, h, c.w, c.h)
		}
	}
	w, h := SeparatorSize(m)
	eq(t, [2]int{w, h}, [2]int{0, 13})
	// at 200 % everything doubles
	w, h = Size(row("normal", 42), MetricsAt(192), fixedMeasurer{192})
	eq(t, [2]int{w, h}, [2]int{640, 128})
	// a large menu font makes the items taller, never the text cut
	big := fixedMeasurer{192}
	w, h = Size(row("normal", 42), m, big)
	if h < 2*32+8 {
		t.Fatalf("row %d high with 32 px lines", h)
	}
	if _, h = Size(Item{Kind: "action", Text: "Quit"}, m, big); h < 32 {
		t.Fatalf("action %d high with 32 px lines", h)
	}
}

func TestLayoutHeader(t *testing.T) {
	m := MetricsAt(96)
	ops := Layout(header(), m, tm96, 350, 32, LightPalette, false)
	eq(t, ops[0], Op{Kind: Fill, W: 350, H: 32, Colour: LightPalette.Background})
	pic := find(t, ops, Pixels, "")
	eq(t, [4]int{pic.X, pic.Y, pic.W, pic.H}, [4]int{16, 7, 18, 18})
	eq(t, pic.Opacity, uint8(255))
	if !bytes.Equal(pic.Pixels, HeaderPicture("claude", 18)) {
		t.Fatal("not Clawd")
	}
	name := find(t, ops, Text, "Claude")
	eq(t, [5]int{name.X, name.Y, name.H, int(name.Font), int(name.Align)}, [5]int{16 + 18 + 8, 0, 32, int(Bold), int(Left)})
	eq(t, name.Colour, LightPalette.Text)
	dim := Dim(LightPalette.Text, LightPalette.Background)
	upd := find(t, ops, Text, "Updated 18:52")
	eq(t, [3]int{upd.X + upd.W, int(upd.Align), int(upd.Font)}, [3]int{350 - 16, int(Right), int(Regular)})
	eq(t, upd.Colour, dim)
	acc := find(t, ops, Text, "team-a@… · 3 PCs")
	eq(t, acc.X, name.X+name.W+8)
	eq(t, acc.Ellipsis, true)
	eq(t, acc.X+acc.W, upd.X-8)
	eq(t, acc.Colour, dim)
	// selected (the keyboard reaches it): a header is never highlighted
	if sel := Layout(header(), m, tm96, 350, 32, LightPalette, true); sel[0].Colour != LightPalette.Background {
		t.Fatalf("header highlighted: %+v", sel[0])
	}
	// Codex: the ring
	cx := Item{Kind: "header", Provider: "codex", Name: "Codex", Account: "bob@… · 1 PC · Plus", Updated: "Updated 18:52"}
	if p := find(t, Layout(cx, m, tm96, 350, 32, LightPalette, false), Pixels, ""); !bytes.Equal(p.Pixels, HeaderPicture("codex", 18)) {
		t.Fatal("not the ring")
	}
	// problem line under it, in the problem colour of the mode
	pr := Item{Kind: "header", Provider: "codex", Name: "Codex", Problem: "Sign-in expired: open Codex to renew it"}
	for _, pal := range []Palette{LightPalette, DarkPalette} {
		ops := Layout(pr, m, tm96, 350, 60, pal, false)
		p := find(t, ops, Text, pr.Problem)
		eq(t, [4]int{p.X, p.Y, p.H, int(p.Font)}, [4]int{16, 32 + 6, 16, int(Regular)})
		eq(t, p.Colour, pal.Problem())
	}
	// stale: faded toward the background, the picture at 0.6
	st := header()
	st.Stale = true
	ops = Layout(st, m, tm96, 350, 32, DarkPalette, false)
	eq(t, find(t, ops, Pixels, "").Opacity, uint8(153))
	eq(t, find(t, ops, Text, "Claude").Colour, Fade(DarkPalette.Text, DarkPalette.Background, StaleOpacity))
}

func TestLayoutRow(t *testing.T) {
	m := MetricsAt(96)
	ops := Layout(row("normal", 42), m, tm96, 350, 64, LightPalette, false)
	eq(t, ops[0].Colour, LightPalette.Background)
	lbl := find(t, ops, Text, "5-hour")
	eq(t, [4]int{lbl.X, lbl.Y, lbl.H, int(lbl.Font)}, [4]int{16, 5, 16, int(Regular)})
	pc := find(t, ops, Text, "42%")
	eq(t, [4]int{pc.X + pc.W, pc.Y, int(pc.Align), int(pc.Font)}, [4]int{350 - 16, 5, int(Right), int(Bold)})
	eq(t, pc.Colour, LightPalette.Text)
	bar := find(t, ops, Pixels, "")
	eq(t, [4]int{bar.X, bar.Y, bar.W, bar.H}, [4]int{16, 5 + 16 + 7, 350 - 32, 8})
	if !bytes.Equal(bar.Pixels, BarPixels(318, 8, FillWidth(pf(42), 318, 8), Normal, Track)) {
		t.Fatal("bar pixels")
	}
	det := find(t, ops, Text, "Resets Fri 21:20 · in 2h 20m")
	eq(t, [2]int{det.X, det.Y}, [2]int{16, 28 + 8 + 6})
	eq(t, det.Colour, Dim(LightPalette.Text, LightPalette.Background))
	// critical: the percent in the critical colour, the bar too
	ops = Layout(row("critical", 100), m, tm96, 350, 64, DarkPalette, false)
	eq(t, find(t, ops, Text, "42%").Colour, Critical)
	if b := find(t, ops, Pixels, ""); !bytes.Equal(b.Pixels, BarPixels(318, 8, 318, Critical, Track)) {
		t.Fatal("critical bar")
	}
	// no value: an empty track
	nv := Item{Kind: "row", Label: "Weekly", PercentText: "–%", Level: "none"}
	if b := find(t, Layout(nv, m, tm96, 350, 64, LightPalette, false), Pixels, ""); !bytes.Equal(b.Pixels, BarPixels(318, 8, 0, Normal, Track)) {
		t.Fatal("empty track")
	}
	// under the pointer: the highlight, its text colour
	ops = Layout(row("normal", 42), m, tm96, 350, 64, LightPalette, true)
	eq(t, ops[0].Colour, LightPalette.Highlight)
	eq(t, find(t, ops, Text, "5-hour").Colour, LightPalette.HighlightText)
	eq(t, find(t, ops, Text, "Resets Fri 21:20 · in 2h 20m").Colour, Dim(LightPalette.HighlightText, LightPalette.Highlight))
	// stale: the bar at 0.6
	st := row("normal", 42)
	st.Stale = true
	eq(t, find(t, Layout(st, m, tm96, 350, 64, LightPalette, false), Pixels, "").Opacity, uint8(153))
	// a taller item: the same block, centred
	lbl = find(t, Layout(row("normal", 42), m, tm96, 350, 74, LightPalette, false), Text, "5-hour")
	eq(t, lbl.Y, 10)
}

func TestLayoutTextItems(t *testing.T) {
	m := MetricsAt(96)
	act := Item{Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true}
	ops := Layout(act, m, tm96, 350, 30, LightPalette, false)
	tx := find(t, ops, Text, "Start at login")
	eq(t, [4]int{tx.X, tx.Y, tx.H, int(tx.Font)}, [4]int{16 + 22, 0, 30, int(Regular)})
	eq(t, tx.Colour, LightPalette.Text)
	ck := find(t, ops, Pixels, "")
	eq(t, [4]int{ck.X, ck.Y, ck.W, ck.H}, [4]int{16 + 5, 9, 12, 12})
	if !bytes.Equal(ck.Pixels, CheckPixels(12, LightPalette.Text)) {
		t.Fatal("check mark")
	}
	act.Checked = false
	for _, o := range Layout(act, m, tm96, 350, 30, LightPalette, false) {
		if o.Kind == Pixels {
			t.Fatal("a check mark although not checked")
		}
	}
	// one hover rule: enabled items get the highlight, disabled ones never
	eq(t, Layout(act, m, tm96, 350, 30, DarkPalette, true)[0].Colour, DarkPalette.Highlight)
	off := Item{Kind: "action", Text: "Refreshing…"}
	ops = Layout(off, m, tm96, 350, 30, DarkPalette, true)
	eq(t, ops[0].Colour, DarkPalette.Background)
	eq(t, find(t, ops, Text, "Refreshing…").Colour, Dim(DarkPalette.Text, DarkPalette.Background))
	// info lines: no check column; the server line dim, the prompt not
	srv := find(t, Layout(Item{Kind: "info", Text: "Server connected", Dim: true}, m, tm96, 350, 30, LightPalette, true), Text, "Server connected")
	eq(t, srv.X, 16)
	eq(t, srv.Colour, Dim(LightPalette.Text, LightPalette.Background))
	pr := find(t, Layout(Item{Kind: "info", Text: "Sign in to Claude Code or Codex"}, m, tm96, 350, 30, LightPalette, false), Text, "Sign in to Claude Code or Codex")
	eq(t, pr.Colour, LightPalette.Text)
	more := find(t, Layout(Item{Kind: "more", Label: "More limits on the dashboard"}, m, tm96, 350, 30, LightPalette, false), Text, "More limits on the dashboard")
	eq(t, more.X, 16)
	// the separator: a line across the content, in the middle
	ops = SeparatorOps(m, 350, 13, DarkPalette)
	eq(t, ops, []Op{{Kind: Fill, W: 350, H: 13, Colour: DarkPalette.Background}, {Kind: Fill, X: 16, Y: 6, W: 318, H: 1, Colour: DarkPalette.Separator()}})
}

// Every op lies inside its item, for every kind, at every DPI, at the
// measured width and wider (Windows adds a gutter to the measured width).
func TestLayoutStaysInside(t *testing.T) {
	items := []Item{header(), {Kind: "header", Provider: "codex", Name: "Codex", Account: strings.Repeat("long ", 30), Updated: "Updated 18:52", Problem: strings.Repeat("problem ", 20)},
		row("critical", 100), row("normal", 1), {Kind: "row", Label: strings.Repeat("L", 60), PercentText: "–%", Level: "none"},
		{Kind: "more", Label: "More limits on the dashboard"}, {Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true},
		{Kind: "info", Text: "Server not reachable · this PC fetches every 30 min", Dim: true}}
	for _, dpi := range []int{96, 120, 144, 192} {
		m, tm := MetricsAt(dpi), fixedMeasurer{dpi}
		for _, it := range items {
			w, h := Size(it, m, tm)
			for _, extra := range []int{0, 43} {
				for _, o := range Layout(it, m, tm, w+extra, h, LightPalette, true) {
					if o.X < 0 || o.Y < 0 || o.W < 0 || o.H < 0 || o.X+o.W > w+extra || o.Y+o.H > h {
						t.Fatalf("dpi %d %s: op %+v outside %dx%d", dpi, it.Kind, o, w+extra, h)
					}
					if o.Kind == Pixels && len(o.Pixels) != o.W*o.H*4 {
						t.Fatalf("dpi %d %s: %d bytes for %dx%d", dpi, it.Kind, len(o.Pixels), o.W, o.H)
					}
				}
			}
		}
	}
}

func TestWrap(t *testing.T) {
	eq(t, Wrap("a bb ccc dddd", Regular, 7*6, tm96), []string{"a bb", "ccc", "dddd"})
	eq(t, Wrap("short", Regular, 300, tm96), []string{"short"})
	eq(t, Wrap("", Regular, 300, tm96), []string(nil))
	// a word wider than the line stays whole on its own line
	eq(t, Wrap("x "+strings.Repeat("y", 10)+" z", Regular, 21, tm96), []string{"x", strings.Repeat("y", 10), "z"})
}

func TestHeaderPicture(t *testing.T) {
	for _, size := range []int{18, 23, 36} {
		cl := HeaderPicture("claude", size)
		eq(t, len(cl), size*size*4)
		// transparent corners, Clawd's colour in the middle of its body
		eq(t, bgra(cl, size, 0, 0), [4]int{0, 0, 0, 0})
		if p := bgra(cl, size, size/2, size/2); p != [4]int{0x57, 0x77, 0xd9, 255} {
			t.Fatalf("size %d: middle %v", size, p)
		}
		rg := HeaderPicture("codex", size)
		eq(t, bgra(rg, size, size/2, size/2), [4]int{0, 0, 0, 0}) // the ring's hole
		if p := bgra(rg, size, size/2, 1); p[3] < 200 || p[2] < 0xc0 {
			t.Fatalf("size %d: ring top %v", size, p)
		}
	}
}

func TestCheckPixels(t *testing.T) {
	b := CheckPixels(12, color.NRGBA{0, 0, 0, 0xff})
	eq(t, len(b), 12*12*4)
	opaque := 0
	for i := 0; i < len(b); i += 4 {
		if b[i] > b[i+3] || b[i+1] > b[i+3] || b[i+2] > b[i+3] {
			t.Fatal("not premultiplied")
		}
		if b[i+3] > 200 {
			opaque++
		}
	}
	if opaque < 10 {
		t.Fatalf("%d opaque pixels", opaque)
	}
	eq(t, bgra(b, 12, 0, 0)[3], 0)
	eq(t, bgra(b, 12, 11, 11)[3], 0)
}

// The fixtures' items: every one has a size and draws.
func TestFixtureItemsLayOut(t *testing.T) {
	for _, in := range []tray.MenuInputs{panelfixture.Combined(), panelfixture.States()} {
		for id, it := range Items(tray.BuildPanel(in)) {
			w, h := Size(it, MetricsAt(96), tm96)
			if w != 320 || h < 32 {
				t.Fatalf("%s: %dx%d", id, w, h)
			}
			if len(Layout(it, MetricsAt(96), tm96, w, h, LightPalette, false)) < 2 {
				t.Fatalf("%s draws nothing", id)
			}
		}
	}
}
