//go:build darwin && cgo

package trayhelper

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/systray"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel/panelcheck"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// AppKit draws on the main thread: the tests run on their goroutines and
// hand drawing to the main one (locked to the main thread by the tray
// library's init), which waits for it here. A closure run there must not
// call t.Fatal or t.FailNow (or panic): that would end the main
// goroutine's loop and leave the next onMain waiting until go test's
// time-out; the loop ends the test binary at once instead, saying why
// (review B3-m12).
var mainCalls = make(chan func())

func TestMain(m *testing.M) {
	done := make(chan int, 1)
	go func() { done <- m.Run() }()
	for {
		select {
		case f := <-mainCalls:
			onMainThread(f)
		case code := <-done:
			os.Exit(code)
		}
	}
}

func onMainThread(f func()) {
	returned := false
	defer func() {
		if !returned {
			fmt.Fprintf(os.Stderr, "FAIL: a call on the main thread did not return (panic %v, or t.FailNow/runtime.Goexit inside onMain)\n", recover())
			os.Exit(1)
		}
	}()
	f()
	returned = true
}

func onMain(f func()) {
	ran := make(chan struct{})
	mainCalls <- func() {
		defer close(ran)
		f()
	}
	<-ran
}

// traced is what one view drew (its steps, the metrics and the text
// missing from them, judged on the main thread with the real fonts; the
// lines a header's problem wraps to there).
type traced struct {
	ops          []panel.Op
	m            panel.Metrics
	w, h         int
	missing      []string
	problemLines int
}

// macDrawer is the macOS drawer, registered with the tray library, that
// keeps each view's steps.
func macDrawer(t *testing.T) (*viewDrawer, map[uint32]*traced) {
	t.Helper()
	d, ok := newItemDrawer().(*viewDrawer)
	if !ok || d == nil {
		t.Fatal("no macOS drawer")
	}
	t.Cleanup(func() { systray.SetPanelViewer(nil) })
	seen := map[uint32]*traced{}
	d.trace = func(id uint32, ops []panel.Op, m panel.Metrics, tm panel.Measurer, w, h int) {
		it, _ := d.item(id)
		seen[id] = &traced{ops: ops, m: m, w: w, h: h, missing: panelcheck.Missing(it, ops, m, tm, w, h),
			problemLines: len(panel.Wrap(it.Problem, panel.Regular, m.Width-2*m.Pad, tm))}
	}
	return d, seen
}

// render draws id's view alone (on the main thread) and decodes it.
func render(t *testing.T, id uint32, width int, dark bool, scale int) *image.NRGBA {
	t.Helper()
	var b []byte
	var err error
	onMain(func() { b, err = systray.RenderPanelViewPNG(id, width, dark, scale) })
	if err != nil {
		t.Fatalf("view %d: %v", id, err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("view %d: %v", id, err)
	}
	out := image.NewNRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

// macPanel is a fixture's panel as the macOS menu's views draw it: each
// view item in menu order, all as wide as the widest (the menu makes them
// so), a separator's gap after each section; the actions are the menu's
// own items and not in the picture.
type macPanel struct {
	pic   *image.NRGBA
	top   map[string]int // each item's top in the picture
	ids   map[string]uint32
	items map[string]panel.Item
	seen  map[uint32]*traced
	width int
	bg    color.NRGBA // the window background, from a view that draws nothing
}

func drawMacPanel(t *testing.T, in tray.MenuInputs, dark bool, scale int) macPanel {
	t.Helper()
	d, seen := macDrawer(t)
	items := macItems(in)
	shape := sectionsShown(fixtureView(in).Items)
	var order []string
	ids := map[string]uint32{}
	n := uint32(1)
	for _, id := range trayproto.ItemIDs {
		if it, ok := items[id]; ok {
			ids[id] = n
			d.Set(n, &it)
			order = append(order, id)
			n++
		}
		if sec := trayproto.Section(id); sec != "" && id == "p."+sec {
			for _, s := range shape {
				if s == sec {
					order = append(order, "-")
				}
			}
		}
	}
	width := 0
	for _, id := range order {
		if id != "-" {
			width = max(width, render(t, ids[id], 0, dark, scale).Bounds().Dx()/scale)
		}
	}
	gap := panel.MetricsAt(96).Separator * scale
	var pics []*image.NRGBA
	height := 0
	for _, id := range order {
		if id == "-" {
			height += gap
			pics = append(pics, nil)
			continue
		}
		p := render(t, ids[id], width, dark, scale)
		if p.Bounds().Dx() != width*scale {
			t.Fatalf("%s: drawn %d px wide, want the menu's %d", id, p.Bounds().Dx(), width*scale)
		}
		pics = append(pics, p)
		height += p.Bounds().Dy()
	}
	out := image.NewNRGBA(image.Rect(0, 0, width*scale, height))
	// the window background under the views: a view that draws nothing
	// (an id the drawer does not know), not a pixel of a view (review
	// B3-m8: a view that filled its background would set it)
	bg := render(t, 999, width, dark, scale).NRGBAAt(1, 1)
	top := map[string]int{}
	y := 0
	for i, id := range order {
		if id == "-" {
			for yy := y; yy < y+gap; yy++ {
				for x := 0; x < width*scale; x++ {
					out.SetNRGBA(x, yy, bg)
				}
			}
			y += gap
			continue
		}
		top[id] = y
		p := pics[i]
		for yy := 0; yy < p.Bounds().Dy(); yy++ {
			for x := 0; x < p.Bounds().Dx(); x++ {
				out.SetNRGBA(x, y+yy, p.NRGBAAt(x, yy))
			}
		}
		y += p.Bounds().Dy()
	}
	return macPanel{pic: out, top: top, ids: ids, items: items, seen: seen, width: width, bg: bg}
}

func (p macPanel) save(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, p.pic); err != nil {
		t.Fatal(err)
	}
}

// at is the picture's pixel at x, y of item id (opaque: the views are
// drawn over the window background).
func (p macPanel) at(id string, x, y int) color.NRGBA { return p.pic.NRGBAAt(x, p.top[id]+y) }

func (p macPanel) op(t *testing.T, id string, kind panel.OpKind, text string) panel.Op {
	t.Helper()
	tr := p.seen[p.ids[id]]
	if tr == nil {
		t.Fatalf("%s was not drawn", id)
	}
	return opOfMac(t, tr.ops, kind, text)
}

func opOfMac(t *testing.T, ops []panel.Op, kind panel.OpKind, text string) panel.Op {
	t.Helper()
	for _, o := range ops {
		if o.Kind == kind && (kind != panel.Text || o.Text == text) {
			return o
		}
	}
	t.Fatalf("no op %v %q", kind, text)
	return panel.Op{}
}

func nearMac(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) bool { v := int(x) - int(y); return v >= -tol && v <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
}

func absMac(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// opaque is c drawn over bg.
func opaque(c, bg color.NRGBA) color.NRGBA {
	c2 := panel.Fade(color.NRGBA{c.R, c.G, c.B, 0xff}, bg, float64(c.A)/255)
	c2.A = 0xff
	return c2
}

// Plan Task 9, PR-21: the fixtures drawn as the macOS menu's views draw
// them, light and dark, at 1× (points): the bars' fill to FillWidth and
// then the track, the critical bar across and its percent in red, the
// header pictures (F7), the stale group at 0.6, the problem line in the
// appearance's colour, every text whole (B-I1), every view at least 320
// wide. That every view is as wide as the widest holds here by
// construction (the test asks for that width: it proves only that a view
// is drawn at the width it is given); the real menu's widths are judged on
// CI by tools/macmenu (panel.mac_menu). With PANEL_PICTURE_DIR set the
// four pictures are written there (CI keeps them for the owner).
func TestPanelViewPictureMac(t *testing.T) {
	dir := os.Getenv("PANEL_PICTURE_DIR")
	m := panel.MetricsAt(96)
	for _, fx := range []struct {
		name string
		in   tray.MenuInputs
	}{{"combined", panelfixture.Combined()}, {"states", panelfixture.States()}} {
		for _, dark := range []bool{false, true} {
			mode := map[bool]string{false: "light", true: "dark"}[dark]
			where := fx.name + " " + mode
			p := drawMacPanel(t, fx.in, dark, 1)
			if dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				p.save(t, filepath.Join(dir, fmt.Sprintf("panel-%s-%s.png", fx.name, mode)))
			}
			if p.width < m.Width {
				t.Fatalf("%s: the views are %d pt wide, under %d", where, p.width, m.Width)
			}
			for id, n := range p.ids {
				tr := p.seen[n]
				if tr == nil {
					t.Fatalf("%s: %s was not drawn", where, id)
				}
				if len(tr.missing) > 0 {
					t.Fatalf("%s: %s: %v", where, id, tr.missing)
				}
				if tr.w != p.width {
					t.Fatalf("%s: %s drawn %d wide, not the menu's %d", where, id, tr.w, p.width)
				}
			}
			bg := p.bg
			for _, xy := range [][2]int{{1, 1}, {p.width - 2, 1}, {p.width / 2, 2}} {
				if got := p.at("h.claude", xy[0], xy[1]); got != bg {
					t.Fatalf("%s: the header fills its background: %v at %v, the window's %v", where, got, xy, bg)
				}
			}
			trackOn := func(over color.NRGBA) color.NRGBA { return opaque(panel.Track, over) }
			stale := fx.name == "states"
			for _, id := range []string{"r.claude.0", "r.claude.1", "r.claude.2"} {
				bar := p.op(t, id, panel.Pixels, "")
				y := bar.Y + bar.H/2
				it := p.items[id]
				fill := panel.FillWidth(it.Percent, bar.W, m.Bar)
				want, track := panel.Normal, trackOn(bg)
				if stale {
					want, track = panel.Fade(panel.Normal, bg, panel.StaleOpacity), panel.Fade(track, bg, panel.StaleOpacity)
				}
				for x := bar.X + m.Bar/2; x <= bar.X+fill-2; x++ {
					if got := p.at(id, x, y); !nearMac(got, want, 3) {
						t.Fatalf("%s %s: fill at %d is %v, want %v", where, id, x-bar.X, got, want)
					}
				}
				for x := bar.X + fill + 1; x < bar.X+bar.W-m.Bar/2; x++ {
					if got := p.at(id, x, y); !nearMac(got, track, 3) {
						t.Fatalf("%s %s: track at %d (fill %d) is %v, want %v", where, id, x-bar.X, fill, got, track)
					}
				}
			}
			if fx.name == "combined" {
				bar := p.op(t, "r.codex.0", panel.Pixels, "")
				y := bar.Y + bar.H/2
				for x := bar.X + m.Bar/2; x < bar.X+bar.W-m.Bar/2; x++ {
					if got := p.at("r.codex.0", x, y); !nearMac(got, panel.Critical, 3) {
						t.Fatalf("%s: Codex 5-hour at %d is %v", where, x, got)
					}
				}
				pc := p.op(t, "r.codex.0", panel.Text, "100%")
				dist := func(a, b color.NRGBA) int {
					return absMac(int(a.R)-int(b.R)) + absMac(int(a.G)-int(b.G)) + absMac(int(a.B)-int(b.B))
				}
				label := opaque(macColour(t, dark), bg)
				red := 0
				for yy := pc.Y; yy < pc.Y+pc.H; yy++ {
					for xx := pc.X; xx < pc.X+pc.W; xx++ {
						c := p.at("r.codex.0", xx, yy)
						if dc := dist(c, panel.Critical); dc < dist(c, panel.Normal) && dc < dist(c, label) && dc < dist(c, bg) {
							red++
						}
					}
				}
				if red < 8 {
					t.Fatalf("%s: %d pixels of the critical percent are in its colour", where, red)
				}
			}
			for _, id := range []string{"h.claude", "h.codex"} {
				pic := p.op(t, id, panel.Pixels, "")
				want := panel.Normal
				if stale && id == "h.claude" {
					want = panel.Fade(want, bg, panel.StaleOpacity)
				}
				n := 0
				for yy := pic.Y; yy < pic.Y+pic.H; yy++ {
					for xx := pic.X; xx < pic.X+pic.W; xx++ {
						if nearMac(p.at(id, xx, yy), want, 3) {
							n++
						}
					}
				}
				if n < 12 {
					t.Fatalf("%s %s: %d pixels of the picture in %v", where, id, n, want)
				}
			}
			if stale {
				prob := tray.BuildPanel(fx.in).Sections[1].Problem
				want := map[bool]color.NRGBA{false: panel.ProblemOnLight, true: panel.ProblemOnDark}[dark]
				var pr panel.Op
				for _, o := range p.seen[p.ids["h.codex"]].ops {
					if o.Kind == panel.Text && o.Text != "" && strings.HasPrefix(prob, o.Text) && o.Y >= m.Header {
						pr = o
						break
					}
				}
				if pr.W == 0 {
					t.Fatalf("%s: no problem line %q drawn", where, prob)
				}
				best := 999
				for yy := pr.Y; yy < pr.Y+pr.H; yy++ {
					for xx := pr.X; xx < pr.X+pr.W; xx++ {
						c := p.at("h.codex", xx, yy)
						best = min(best, max(absMac(int(c.R)-int(want.R)), absMac(int(c.G)-int(want.G)), absMac(int(c.B)-int(want.B))))
					}
				}
				if best > 40 {
					t.Fatalf("%s: the problem line's colour is %d away from %v", where, best, want)
				}
			}
			t.Logf("%s: %dx%d pt", where, p.pic.Bounds().Dx(), p.pic.Bounds().Dy())
		}
	}
}

// macColour is the label colour's opaque look in the appearance (black or
// white, at the system's opacity).
func macColour(t *testing.T, dark bool) color.NRGBA {
	t.Helper()
	if dark {
		return color.NRGBA{0xff, 0xff, 0xff, 0xd8}
	}
	return color.NRGBA{0, 0, 0, 0xd8}
}

// Plan Task 9: a header with a problem (States' h.codex) draws it under
// itself in the problem's colour and is taller by exactly the problem
// line (Problem; each further line it wraps to a line more); a header with
// a long account is wider than 320 and cuts nothing (A-I1); a line that
// cannot be clicked wraps at the panel's width instead of widening it
// (A-I2).
func TestHeaderViewDrawsTheProblem(t *testing.T) {
	m := panel.MetricsAt(96)
	d, seen := macDrawer(t)
	states := macItems(panelfixture.States())
	prob := states["h.codex"]
	if prob.Problem == "" {
		t.Fatal("States' h.codex has no problem line")
	}
	plain := prob
	plain.Problem = ""
	long := plain
	long.Account = "firstname.lastname.…@… · 12 PCs · Enterprise"
	long.Updated = "Updated Mon 15:30"
	server := panel.Item{Kind: "info", Text: "Server connected · Claude from DESKTOP-ABCDEFG · Codex from DESKTOP-HIJKLMN", Dim: true}
	for i, it := range []panel.Item{plain, prob, long, server} {
		it := it
		d.Set(uint32(i+1), &it)
	}
	a, b := render(t, 1, 0, false, 1), render(t, 2, 0, false, 1)
	lines := seen[2].problemLines
	got := b.Bounds().Dy() - a.Bounds().Dy()
	switch {
	case lines < 1:
		t.Fatalf("the problem %q wraps to %d lines", prob.Problem, lines)
	case lines == 1 && got != m.Problem:
		t.Fatalf("the header with a one-line problem is %d pt taller, want %d", got, m.Problem)
	case lines > 1 && got < m.Problem+(lines-1)*12:
		t.Fatalf("the header with a %d-line problem is %d pt taller, want at least %d", lines, got, m.Problem+(lines-1)*12)
	}
	n := 0
	for y := a.Bounds().Dy(); y < b.Bounds().Dy(); y++ {
		for x := 0; x < b.Bounds().Dx(); x++ {
			if nearMac(b.NRGBAAt(x, y), panel.ProblemOnLight, 40) {
				n++
			}
		}
	}
	if n < 8 {
		t.Fatalf("%d pixels of the problem line in %v", n, panel.ProblemOnLight)
	}
	l := render(t, 3, 0, false, 1)
	if l.Bounds().Dx() <= m.Width {
		t.Fatalf("the long account's header is %d pt wide: its texts would be cut", l.Bounds().Dx())
	}
	if miss := seen[3].missing; len(miss) > 0 {
		t.Fatalf("long account: %v", miss)
	}
	s := render(t, 4, 0, false, 1)
	if s.Bounds().Dx() != m.Width || s.Bounds().Dy() <= m.Item {
		t.Fatalf("the two-host server line is %dx%d pt: not wrapped at the panel's width", s.Bounds().Dx(), s.Bounds().Dy())
	}
	if miss := seen[4].missing; len(miss) > 0 {
		t.Fatalf("server line: %v", miss)
	}
	t.Logf("header %d, with problem %d (%d lines), long account %d wide, server line %dx%d", a.Bounds().Dy(), b.Bounds().Dy(), lines, l.Bounds().Dx(), s.Bounds().Dx(), s.Bounds().Dy())
}

// Review A3-M10: an Objective-C exception raised while a view draws is
// caught in the tray library (it never unwinds through Go) and stops the
// drawing as a panic does: the drawer fails, so the helper shows its text
// menu.
func TestObjectiveCExceptionStopsTheDrawing(t *testing.T) {
	d, _ := macDrawer(t)
	it := macItems(panelfixture.Combined())["h.claude"]
	d.Set(1, &it)
	render(t, 1, 0, false, 1)
	if d.Failed() {
		t.Fatal("failed before the exception")
	}
	onMain(systray.RaiseInNextPanelViewText)
	render(t, 1, 0, false, 1)
	if !d.Failed() {
		t.Fatal("the drawer did not hear of the exception")
	}
}

// On a Retina screen the views draw at two pixels per point: the same
// layout, the bar's fill twice as long, every text whole.
func TestPanelViewAtTwiceTheScale(t *testing.T) {
	p1 := drawMacPanel(t, panelfixture.Combined(), false, 1)
	p2 := drawMacPanel(t, panelfixture.Combined(), false, 2)
	eq(t, p2.pic.Bounds().Dx(), 2*p1.pic.Bounds().Dx())
	eq(t, p2.pic.Bounds().Dy(), 2*p1.pic.Bounds().Dy())
	for id, n := range p2.ids {
		if miss := p2.seen[n].missing; len(miss) > 0 {
			t.Fatalf("%s at 2×: %v", id, miss)
		}
	}
	// the end of r.claude.0's fill, from the picture
	end := func(p macPanel, scale int) int {
		bar := p.op(t, "r.claude.0", panel.Pixels, "")
		y := bar.Y + bar.H/2
		last := -1
		for x := bar.X; x < bar.X+bar.W; x++ {
			if nearMac(p.at("r.claude.0", x, y), panel.Normal, 3) {
				last = x - bar.X
			}
		}
		return last
	}
	e1, e2 := end(p1, 1), end(p2, 2)
	if e1 <= 0 || absMac(e2-2*e1) > 2 {
		t.Fatalf("the fill ends at %d px at 2×, %d at 1×", e2, e1)
	}
}
