package trayhelper

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel/panelcheck"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// fakeCanvas measures 7 px a character (the heavier faces 8) and 16 px
// lines per point, times its scale, and records what it is told to draw.
type fakeCanvas struct {
	scale int
	calls []string
	panic bool // measuring panics (a fault in the font code)
}

func (c *fakeCanvas) TextWidth(font int, s string) int {
	if c.panic {
		panic("measuring")
	}
	per := 7
	if font != int(panel.Regular) {
		per = 8
	}
	return per * utf8.RuneCountInString(s) * max(c.scale, 1)
}
func (c *fakeCanvas) LineHeight(int) int {
	if c.panic {
		panic("measuring")
	}
	return 16 * max(c.scale, 1)
}
func (c *fakeCanvas) Fill(x, y, w, h, radius int, col color.NRGBA) {
	c.calls = append(c.calls, fmt.Sprintf("fill %d,%d %dx%d r%d %v", x, y, w, h, radius, col))
}
func (c *fakeCanvas) Text(x, y, w, h int, s string, font int, col color.NRGBA, right, ellipsis bool) {
	c.calls = append(c.calls, fmt.Sprintf("text %d,%d %dx%d %q f%d %v right=%v …=%v", x, y, w, h, s, font, col, right, ellipsis))
}
func (c *fakeCanvas) Pixels(x, y, w, h int, bgra []byte, opacity uint8) {
	c.calls = append(c.calls, fmt.Sprintf("pixels %d,%d %dx%d %d bytes at %d %x", x, y, w, h, len(bgra), opacity, bgra[:min(len(bgra), 16)]))
}

// macColours are a light appearance's resolved system colours.
var macColours = viewColours{Text: color.NRGBA{0, 0, 0, 0xd8}, SecondaryText: color.NRGBA{0, 0, 0, 0x80},
	Highlight: color.NRGBA{0x00, 0x64, 0xe1, 0xff}, HighlightText: color.NRGBA{0xff, 0xff, 0xff, 0xff}}

// macItems are the menu's drawn items of a fixture as the macOS helper
// gives them (no actions).
func macItems(in tray.MenuInputs) map[string]panel.Item {
	v := fixtureView(in)
	byID := map[string]trayproto.Item{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	return drawnItems(*v.Panel, byID, false)
}

// expectedCalls is what Layout says, as the canvas is told it.
func expectedCalls(ops []panel.Op) []string {
	c := &fakeCanvas{}
	for _, o := range ops {
		switch o.Kind {
		case panel.Fill:
			c.Fill(o.X, o.Y, o.W, o.H, o.Radius, o.Colour)
		case panel.Text:
			c.Text(o.X, o.Y, o.W, o.H, o.Text, int(o.Font), o.Colour, o.Align == panel.Right, o.Ellipsis)
		case panel.Pixels:
			c.Pixels(o.X, o.Y, o.W, o.H, o.Pixels, o.Opacity)
		}
	}
	return c.calls
}

// Plan Task 9, F11: a view's size is panel.Size at 96 DPI in points with
// the menu's fonts, and what it draws is exactly panel.Layout's steps at
// 96 × its scale DPI, in the system's colours over the menu's own
// background (nothing filled unless highlighted, the inset rounded
// highlight), carried out one by one; every text whole (B-I1) at both
// scales.
func TestViewItemsCarryOutThePanelsLayout(t *testing.T) {
	d := newViewDrawer()
	items := macItems(panelfixture.States())
	if _, ok := items["refresh"]; ok {
		t.Fatal("an action among the macOS view items")
	}
	ids := map[string]uint32{}
	n := uint32(1)
	for id, it := range items {
		it := it
		d.Set(n, &it)
		ids[id] = n
		n++
	}
	for id, it := range items {
		w, h := d.ViewSize(ids[id], &fakeCanvas{scale: 1})
		ww, wh := panel.Size(it, panel.MetricsAt(96), canvasMeasurer{&fakeCanvas{scale: 1}})
		eq(t, [2]int{w, h}, [2]int{ww, wh})
		for _, scale := range []int{1, 2} {
			for _, hl := range []bool{false, true} {
				c := &fakeCanvas{scale: scale}
				pw, ph := (w+20)*scale, h*scale // the menu is wider than the view
				d.DrawView(ids[id], c, pw, ph, scale, macColours, hl)
				m := panel.MetricsAt(96 * scale)
				ops := panel.Layout(it, m, canvasMeasurer{c}, pw, ph, macPalette(macColours), hl)
				if got, want := strings.Join(c.calls, "\n"), strings.Join(expectedCalls(ops), "\n"); got != want {
					t.Fatalf("%s at %d×, highlighted %v:\n%s\nwant\n%s", id, scale, hl, got, want)
				}
				if miss := panelcheck.Missing(it, ops, m, canvasMeasurer{c}, pw, ph); len(miss) > 0 {
					t.Fatalf("%s at %d×: %v", id, scale, miss)
				}
				if !strings.HasPrefix(c.calls[0], "fill 0,0") || !strings.HasSuffix(c.calls[0], "{0 0 0 0}") {
					t.Fatalf("%s: the first step is not the clear background: %s", id, c.calls[0])
				}
				clickable := it.Kind == "row" || it.Kind == "more"
				if hl && clickable {
					if want := fmt.Sprintf("fill %d,0 %dx%d r%d %v", 5*scale, pw-10*scale, ph, 6*scale, macColours.Highlight); c.calls[1] != want {
						t.Fatalf("%s highlighted: %s, want %s", id, c.calls[1], want)
					}
				} else {
					for _, call := range c.calls[1:] {
						if strings.HasPrefix(call, "fill") {
							t.Fatalf("%s (highlighted %v) fills over the menu: %s", id, hl, call)
						}
					}
				}
			}
		}
	}
	// the dim lines are the system's secondary label colour; the stale
	// group is at 0.6 of the label colour's opacity, its dim lines stay
	// the secondary label colour (not faded twice: review A-m3); the
	// problem line is dark amber in a light appearance, amber in a dark
	// one
	c := &fakeCanvas{scale: 1}
	d.DrawView(ids["r.claude.0"], c, 320, 64, 1, macColours, false)
	fade := func(c color.NRGBA) color.NRGBA { c.A = uint8(float64(c.A)*panel.StaleOpacity + 0.5); return c }
	calls := strings.Join(c.calls, "\n")
	if !strings.Contains(calls, fmt.Sprint(fade(macColours.Text))) || !strings.Contains(calls, fmt.Sprint(macColours.SecondaryText)) ||
		strings.Contains(calls, fmt.Sprint(fade(macColours.SecondaryText))) {
		t.Fatalf("stale text not %v, dim text not %v in %v", fade(macColours.Text), macColours.SecondaryText, c.calls)
	}
	for _, dark := range []bool{false, true} {
		cols := macColours
		cols.Dark = dark
		c := &fakeCanvas{scale: 1}
		d.DrawView(ids["h.codex"], c, 320, 64, 1, cols, false)
		want := map[bool]color.NRGBA{false: panel.ProblemOnLight, true: panel.ProblemOnDark}[dark]
		found := false
		for _, call := range c.calls {
			if strings.HasPrefix(call, "text") && strings.Contains(call, fmt.Sprint(want)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("dark %v: the problem line is not %v: %v", dark, want, c.calls)
		}
	}
	// an id it does not know: a text item's size, nothing drawn
	w, h := d.ViewSize(999, &fakeCanvas{scale: 1})
	eq(t, [2]int{w, h}, [2]int{320, 30})
	c = &fakeCanvas{scale: 1}
	d.DrawView(999, c, 320, 30, 1, macColours, false)
	eq(t, len(c.calls), 0)
	eq(t, d.DrawsActions(), false)
}

// What VoiceOver says for a view item: its texts.
func TestViewItemsHaveLabels(t *testing.T) {
	d := newViewDrawer()
	for i, it := range []panel.Item{
		{Kind: "header", Name: "Codex", Account: "user@… · 2 PCs · Plus", Updated: "Updated 19:00", Problem: "Sign-in expired"},
		{Kind: "row", Label: "5-hour", PercentText: "100%", Detail: "Blocked · resets Fri 19:35 · in 35m"},
		{Kind: "info", Text: "Server connected"},
	} {
		it := it
		d.Set(uint32(i+1), &it)
	}
	eq(t, d.ViewLabel(1), "Codex, user@… · 2 PCs · Plus, Updated 19:00, Sign-in expired")
	eq(t, d.ViewLabel(2), "5-hour, 100%, Blocked · resets Fri 19:35 · in 35m")
	eq(t, d.ViewLabel(3), "Server connected")
	eq(t, d.ViewLabel(4), "")
}

// The ruling for an open menu (as A-m14 on Windows): every view draws
// and is sized from what it held when the menu opened, so a new view
// mid-way never gives half old and half new values; a view added to the
// open menu draws what it holds; the next open shows the new values.
func TestOpenMacMenuKeepsItsValues(t *testing.T) {
	d := newViewDrawer()
	a := panel.Item{Kind: "row", Label: "5-hour", PercentText: "42%", Detail: "Resets Fri 21:20 · in 2h 20m", Level: "normal"}
	b := a
	b.PercentText = "57%"
	hd := panel.Item{Kind: "header", Name: "Codex"}
	hdProblem := hd
	hdProblem.Problem = "Sign-in expired: open Codex to renew it"
	d.Set(1, &a)
	d.Set(2, &hd)
	draws := func(id uint32, text string) bool {
		c := &fakeCanvas{scale: 1}
		d.DrawView(id, c, 320, 64, 1, macColours, false)
		return strings.Contains(strings.Join(c.calls, "\n"), fmt.Sprintf("%q", text))
	}
	_, h0 := d.ViewSize(2, &fakeCanvas{scale: 1})
	d.MenuOpening()
	d.Set(1, &b)
	d.Set(2, &hdProblem)
	c := panel.Item{Kind: "info", Text: "Refreshing"}
	d.Set(3, &c)
	if !draws(1, "42%") || draws(1, "57%") {
		t.Fatal("the open menu draws the new value")
	}
	if _, h := d.ViewSize(2, &fakeCanvas{scale: 1}); h != h0 {
		t.Fatalf("the open menu's header was sized anew: %d, was %d", h, h0)
	}
	if !draws(3, "Refreshing") {
		t.Fatal("a view added to the open menu draws nothing")
	}
	d.MenuClosed()
	if !draws(1, "57%") {
		t.Fatal("the new value is not drawn after the menu closed")
	}
	d.MenuOpening()
	if _, h := d.ViewSize(2, &fakeCanvas{scale: 1}); h <= h0 {
		t.Fatalf("the next open does not size the header with its problem line: %d", h)
	}
	d.MenuClosed()
	d.Clear()
	if draws(1, "57%") {
		t.Fatal("a cleared drawer still draws")
	}
}

// Review A-m4 on macOS: a panic in measuring or drawing is caught (it
// would end the helper from inside AppKit), logged once, and nothing is
// drawn any more; the ui then stops drawing (the helper builds the text
// menu, TestTextMenuWhenTheUIStopsDrawing).
func TestMacDrawerPanicStopsTheDrawing(t *testing.T) {
	d := newViewDrawer()
	it := panel.Item{Kind: "row", Label: "5-hour", PercentText: "42%", Level: "normal"}
	d.Set(1, &it)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w, h := d.ViewSize(1, &fakeCanvas{scale: 1, panic: true})
	eq(t, [2]int{w, h}, [2]int{320, 30})
	eq(t, d.Failed(), true)
	c := &fakeCanvas{scale: 1}
	d.DrawView(1, c, 320, 64, 1, macColours, false)
	eq(t, len(c.calls), 0)
	d.DrawView(1, &fakeCanvas{scale: 1, panic: true}, 320, 64, 1, macColours, false)
	log.SetOutput(os.Stderr)
	eq(t, strings.Count(logged.String(), "the tray menu is text from now on"), 1)
}

// Review A3-M10: an Objective-C exception the tray library caught while a
// view was drawn stops the drawing as a panic does: logged once, nothing
// drawn from then on, Failed (the helper shows the text menu).
func TestMacDrawerStopsOnAnObjectiveCException(t *testing.T) {
	d := newViewDrawer()
	it := panel.Item{Kind: "row", Label: "5-hour", PercentText: "42%", Level: "normal"}
	d.Set(1, &it)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	eq(t, d.Failed(), false)
	d.ViewFailed("NSInternalInconsistencyException: a test")
	d.ViewFailed("again")
	eq(t, d.Failed(), true)
	c := &fakeCanvas{scale: 1}
	d.DrawView(1, c, 320, 64, 1, macColours, false)
	eq(t, len(c.calls), 0)
	log.SetOutput(os.Stderr)
	eq(t, strings.Count(logged.String(), "the tray menu is text from now on"), 1)
	eq(t, strings.Contains(logged.String(), "NSInternalInconsistencyException: a test"), true)
}

// Review A-m1: the first failure (a panic or an Objective-C exception)
// cancels the open menu, as Windows ends its menu, and tells the helper at
// once (FailedSignal), which then builds the text menu.
func TestMacDrawerFailureClosesTheOpenMenu(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for name, fail := range map[string]func(d *viewDrawer){
		"panic":     func(d *viewDrawer) { d.ViewSize(1, &fakeCanvas{scale: 1, panic: true}) },
		"exception": func(d *viewDrawer) { d.ViewFailed("NSInternalInconsistencyException: a test") },
	} {
		d := newViewDrawer()
		cancels := 0
		d.cancelMenu = func() { cancels++ }
		it := panel.Item{Kind: "row", Label: "5-hour", PercentText: "42%", Level: "normal"}
		d.Set(1, &it)
		sig := d.FailedSignal()
		select {
		case <-sig:
			t.Fatalf("%s: signalled before a failure", name)
		default:
		}
		fail(d)
		fail(d)
		d.ViewFailed("again")
		eq(t, cancels, 1)
		select {
		case <-sig:
		default:
			t.Fatalf("%s: no signal after the failure", name)
		}
		select {
		case <-d.FailedSignal(): // asked after the failure: closed too
		default:
			t.Fatalf("%s: a signal asked for later is open", name)
		}
	}
}

// RF4 on macOS: the helper's goroutine sets the items while the main
// thread opens the menu, sizes and draws them (-race on Linux).
func TestViewDrawerWhileViewChanges(t *testing.T) {
	d := newViewDrawer()
	a := macItems(panelfixture.Combined())
	b := macItems(panelfixture.States())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 500 {
			src := a
			if i%2 == 1 {
				src = b
			}
			for n, id := range []string{"h.codex", "r.codex.0", "server"} {
				it := src[id]
				d.Set(uint32(n+1), &it)
			}
			if i%50 == 49 {
				d.Clear()
			}
		}
	}()
	for i := range 500 {
		if i%7 == 0 {
			d.MenuOpening()
		}
		for id := uint32(1); id <= 3; id++ {
			w, h := d.ViewSize(id, &fakeCanvas{scale: 1})
			d.DrawView(id, &fakeCanvas{scale: 2}, 2*w, 2*h, 2, macColours, i%3 == 0)
			_ = d.ViewLabel(id)
		}
		if i%7 == 3 {
			d.MenuClosed()
		}
	}
	wg.Wait()
}
