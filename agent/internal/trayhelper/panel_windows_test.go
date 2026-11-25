package trayhelper

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// canvas is a 32-bit top-down DIB in a memory DC: what a menu's DC is to
// the drawer.
type canvas struct {
	dc, bmp, old uintptr
	bits         unsafe.Pointer
	w, h         int
}

func newCanvas(t *testing.T, w, h int) *canvas {
	t.Helper()
	c := &canvas{w: w, h: h}
	c.dc, _, _ = pwCreateCompatibleDC.Call(0)
	if c.dc == 0 {
		t.Fatal("CreateCompatibleDC")
	}
	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	c.bmp, _, _ = pwCreateDIBSection.Call(c.dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&c.bits)), 0, 0)
	if c.bmp == 0 || c.bits == nil {
		t.Fatal("CreateDIBSection")
	}
	c.old, _, _ = pwSelectObject.Call(c.dc, c.bmp)
	t.Cleanup(func() {
		pwSelectObject.Call(c.dc, c.old)
		pwDeleteObject.Call(c.bmp)
		pwDeleteDC.Call(c.dc)
	})
	return c
}

func (c *canvas) at(x, y int) color.NRGBA {
	b := unsafe.Slice((*byte)(c.bits), c.w*c.h*4)
	i := (y*c.w + x) * 4
	return color.NRGBA{b[i+2], b[i+1], b[i], 0xff}
}

func (c *canvas) save(t *testing.T, path string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, c.w, c.h))
	for y := range c.h {
		for x := range c.w {
			img.SetNRGBA(x, y, c.at(x, y))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func near(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) bool { v := int(x) - int(y); return v >= -tol && v <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
}

func fixed(th menuTheme, light panel.Palette) func() (menuTheme, panel.Palette) {
	return func() (menuTheme, panel.Palette) { return th, light }
}

// calcWidth is the width DrawTextW gives s in the drawer's font at dpi.
func calcWidth(t *testing.T, d *winDrawer, dc uintptr, dpi uint32, f panel.Font, s string) int {
	t.Helper()
	u, _ := windows.UTF16FromString(s)
	d.mu.Lock()
	fonts := d.fontsAt(dpi)
	d.mu.Unlock()
	old, _, _ := pwSelectObject.Call(dc, fonts.get(f))
	defer pwSelectObject.Call(dc, old)
	r := systray.Rect{Right: 10000, Bottom: 1000}
	pwDrawTextW.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), dtCalcRect|dtSingleLine|dtNoPrefix)
	return int(r.Right - r.Left)
}

// RF5: what Windows is told an item measures is what the drawing fills,
// at 100, 125, 150 and 200 %; the fixture's texts fit the panel's width.
func TestMeasureMatchesDrawAtEveryDPI(t *testing.T) {
	comb := panel.Items(tray.BuildPanel(panelfixture.Combined()))
	states := panel.Items(tray.BuildPanel(panelfixture.States()))
	pv := tray.BuildPanel(panelfixture.Combined())
	items := map[string]panel.Item{
		"header": comb["h.claude"], "header+problem": states["h.codex"], "row": comb["r.codex.0"],
		"more":   {Kind: "more", Label: "More limits on the dashboard"},
		"action": {Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true},
		"server": {Kind: "info", Text: pv.Server, Dim: true},
	}
	for _, dpi := range []uint32{96, 120, 144, 192} {
		m := panel.MetricsAt(int(dpi))
		d := newWinDrawer(fixed(menuTheme{}, panel.LightPalette))
		c := newCanvas(t, 2*m.Width, 4*m.Row)
		for name, it := range items {
			d.Set(1, &it)
			w, h := d.Measure(1, dpi)
			sw, sh := panel.Size(it, m, d.measurer(c.dc, dpi))
			if int(w) != sw || int(h) != sh {
				t.Fatalf("dpi %d %s: measured %dx%d, layout %dx%d", dpi, name, w, h, sw, sh)
			}
			gutter := int32(dpi * 3 / 10) // Windows widens the item (stage 0: 36 px at 120 DPI)
			d.Draw(1, c.dc, systray.Rect{Right: w + gutter, Bottom: h}, odsSelected, dpi)
			d.mu.Lock()
			ops := d.lastOps
			d.mu.Unlock()
			for _, o := range ops {
				if o.X < 0 || o.Y < 0 || o.X+o.W > int(w+gutter) || o.Y+o.H > int(h) {
					t.Fatalf("dpi %d %s: %+v outside %dx%d", dpi, name, o, w+gutter, h)
				}
			}
		}
		if _, h := d.MeasureSeparator(dpi); int(h) != m.Separator {
			t.Fatalf("dpi %d: separator %d", dpi, h)
		}
		// the fixture's texts fit the panel's width at every DPI
		inner := m.Width - 2*m.Pad
		gap := (8*m.Pad + 8) / 16
		for _, in := range []tray.MenuInputs{panelfixture.Combined(), panelfixture.States()} {
			p := tray.BuildPanel(in)
			for _, s := range p.Sections {
				line := m.Picture + gap + calcWidth(t, d, c.dc, dpi, panel.Bold, s.Name) + gap +
					calcWidth(t, d, c.dc, dpi, panel.Regular, s.Account) + gap + calcWidth(t, d, c.dc, dpi, panel.Regular, s.Updated)
				if line > inner {
					t.Fatalf("dpi %d: %s's header is %d px, the panel has %d", dpi, s.Name, line, inner)
				}
				for _, l := range panel.Wrap(s.Problem, panel.Regular, inner, d.measurer(c.dc, dpi)) {
					if w := calcWidth(t, d, c.dc, dpi, panel.Regular, l); w > inner {
						t.Fatalf("dpi %d: problem line %q is %d px", dpi, l, w)
					}
				}
				for _, r := range s.Rows {
					if w := calcWidth(t, d, c.dc, dpi, panel.Regular, r.Label) + gap + calcWidth(t, d, c.dc, dpi, panel.Bold, r.PercentText); w > inner {
						t.Fatalf("dpi %d: row %q is %d px", dpi, r.Label, w)
					}
					if w := calcWidth(t, d, c.dc, dpi, panel.Regular, r.Detail); w > inner {
						t.Fatalf("dpi %d: %q is %d px", dpi, r.Detail, w)
					}
				}
			}
			for _, a := range p.Actions {
				if w := m.Check + calcWidth(t, d, c.dc, dpi, panel.Regular, a.Text); w > inner {
					t.Fatalf("dpi %d: %q is %d px", dpi, a.Text, w)
				}
			}
			// the server line is never cut: the menu is as wide as it needs
			it := panel.Item{Kind: "info", Text: p.Server, Dim: true}
			d.Set(2, &it)
			if w, _ := d.Measure(2, dpi); int(w) < 2*m.Pad+calcWidth(t, d, c.dc, dpi, panel.Regular, p.Server) {
				t.Fatalf("dpi %d: server line %q measured %d", dpi, p.Server, w)
			}
		}
	}
}

// RF5 with the one-switch ruling: the drawing is in the dark set only in
// dark mode, in the system's menu colours otherwise.
func TestDrawsTheModesColours(t *testing.T) {
	it := panel.Items(tray.BuildPanel(panelfixture.Combined()))["h.claude"]
	light := systemLightPalette()
	for _, c := range []struct {
		th   menuTheme
		want color.NRGBA
	}{{menuTheme{}, light.Background}, {menuTheme{Dark: true}, panel.DarkPalette.Background}} {
		d := newWinDrawer(fixed(c.th, light))
		d.Set(1, &it)
		cv := newCanvas(t, 400, 40)
		d.Draw(1, cv.dc, systray.Rect{Right: 400, Bottom: 32}, 0, 96)
		if got := cv.at(398, 1); got != c.want {
			t.Fatalf("dark %v: background %v, want %v", c.th.Dark, got, c.want)
		}
		rgb, ok := d.MenuBackground()
		eq(t, ok, true)
		eq(t, rgb, colorRef(c.want))
	}
	// the real reading on this PC answers without failing
	th, pal := loadMenuTheme()
	t.Logf("this runner: dark %v, light palette %+v", th.Dark, pal)
}

// RF5: a change of the colour mode is followed; other settings changes do
// not read the mode again.
func TestThemeChangeIsFollowed(t *testing.T) {
	loads, dark := 0, false
	d := newWinDrawer(func() (menuTheme, panel.Palette) {
		loads++
		return menuTheme{Dark: dark}, panel.LightPalette
	})
	eq(t, loads, 1)
	dark = true
	for _, area := range []string{"WindowMetrics", "", "Policy"} {
		d.SettingChanged(area)
	}
	eq(t, loads, 1)
	rgb, _ := d.MenuBackground()
	eq(t, rgb, colorRef(panel.LightPalette.Background))
	d.SettingChanged("ImmersiveColorSet")
	eq(t, loads, 2)
	rgb, _ = d.MenuBackground()
	eq(t, rgb, colorRef(panel.DarkPalette.Background))
	it := panel.Item{Kind: "action", Text: "Quit", Enabled: true}
	d.Set(3, &it)
	cv := newCanvas(t, 400, 40)
	d.Draw(3, cv.dc, systray.Rect{Right: 400, Bottom: 30}, 0, 96)
	eq(t, cv.at(398, 1), panel.DarkPalette.Background)
}

// RF4: drawing while the helper swaps the items' data (the menu open
// across a new view) never fails.
func TestDrawWhileViewChanges(t *testing.T) {
	a := panel.Items(tray.BuildPanel(panelfixture.Combined()))
	b := panel.Items(tray.BuildPanel(panelfixture.States()))
	d := newWinDrawer(fixed(menuTheme{}, panel.LightPalette))
	cv := newCanvas(t, 400, 80)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			src := a
			if i%2 == 1 {
				src = b
			}
			it := src["h.codex"]
			d.Set(1, &it)
			if i%7 == 0 {
				d.Set(1, nil)
			}
		}
	}()
	for range 1000 {
		d.Draw(1, cv.dc, systray.Rect{Right: 400, Bottom: 60}, 0, 96)
	}
	wg.Wait()
}

// panelPicture draws a fixture's whole drawn menu, as the helper builds it
// (drawnItems, a drawn separator after each section), at 96 DPI; it
// returns the canvas and where each item went.
func panelPicture(t *testing.T, in tray.MenuInputs, th menuTheme, light panel.Palette) (*canvas, map[string]systray.Rect, map[string][]panel.Op, panel.Palette) {
	t.Helper()
	v := fixtureView(in)
	byID := map[string]trayproto.Item{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	drawn := drawnItems(*v.Panel, byID)
	shape := sectionsShown(v.Items)
	d := newWinDrawer(fixed(th, light))
	type entry struct {
		id   string
		w, h int32
	}
	var order []entry
	width := int32(0)
	n := uint32(1)
	ids := map[string]uint32{}
	for _, id := range trayproto.ItemIDs {
		if it, ok := drawn[id]; ok {
			ids[id] = n
			d.Set(n, &it)
			w, h := d.Measure(n, 96)
			order = append(order, entry{id, w, h})
			width = max(width, w)
			n++
		}
		if sec := trayproto.Section(id); sec != "" && id == "p."+sec && strings.Contains(strings.Join(shape, " "), sec) {
			_, h := d.MeasureSeparator(96)
			order = append(order, entry{"-", 0, h})
		}
	}
	height := int32(0)
	for _, e := range order {
		height += e.h
	}
	c := newCanvas(t, int(width), int(height))
	rects := map[string]systray.Rect{}
	ops := map[string][]panel.Op{}
	y := int32(0)
	for _, e := range order {
		rc := systray.Rect{Top: y, Right: width, Bottom: y + e.h}
		if e.id == "-" {
			d.DrawSeparator(c.dc, rc, 96)
		} else {
			d.Draw(ids[e.id], c.dc, rc, 0, 96)
			d.mu.Lock()
			ops[e.id] = d.lastOps
			d.mu.Unlock()
			rects[e.id] = rc
		}
		y += e.h
	}
	return c, rects, ops, th.palette(light)
}

func opOf(t *testing.T, ops []panel.Op, kind panel.OpKind, text string) panel.Op {
	t.Helper()
	for _, o := range ops {
		if o.Kind == kind && (kind != panel.Text || o.Text == text) {
			return o
		}
	}
	t.Fatalf("no op %v %q", kind, text)
	return panel.Op{}
}

// The fixtures drawn as the menu draws them, light and dark: the bars'
// fill and track, the critical percent, the header pictures (pre-flight
// F7), the faded group and the problem line. With PANEL_PICTURE_DIR set,
// the four pictures are written there (CI keeps them for the owner).
func TestPanelPictureWindows(t *testing.T) {
	light := systemLightPalette()
	dir := os.Getenv("PANEL_PICTURE_DIR")
	for _, fx := range []struct {
		name string
		in   tray.MenuInputs
	}{{"combined", panelfixture.Combined()}, {"states", panelfixture.States()}} {
		for _, th := range []menuTheme{{}, {Dark: true}} {
			mode := map[bool]string{false: "light", true: "dark"}[th.Dark]
			c, rects, ops, pal := panelPicture(t, fx.in, th, light)
			where := fx.name + " " + mode
			if dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				c.save(t, filepath.Join(dir, fmt.Sprintf("panel-%s-%s.png", fx.name, mode)))
			}
			bg := pal.Background
			if got := c.at(int(rects["h.claude"].Right)-2, int(rects["h.claude"].Top)+1); got != bg {
				t.Fatalf("%s: header background %v, want %v", where, got, bg)
			}
			if got := c.at(int(rects["refresh"].Right)-2, int(rects["refresh"].Top)+1); got != bg {
				t.Fatalf("%s: Refresh now's background %v, want %v", where, got, bg)
			}
			m := panel.MetricsAt(96)
			trackOn := func(over color.NRGBA) color.NRGBA {
				return panel.Fade(color.NRGBA{0x80, 0x80, 0x80, 0xff}, over, float64(panel.Track.A)/255)
			}
			stale := fx.name == "states"
			// Claude's rows: the fill from the track's start to FillWidth, then the track
			for _, id := range []string{"r.claude.0", "r.claude.1", "r.claude.2"} {
				bar := opOf(t, ops[id], panel.Pixels, "")
				rc := rects[id]
				y := int(rc.Top) + bar.Y + bar.H/2
				x0 := int(rc.Left) + bar.X
				it := panel.Items(tray.BuildPanel(fx.in))[id]
				fill := panel.FillWidth(it.Percent, bar.W, m.Bar)
				want, track := panel.Normal, trackOn(bg)
				if stale {
					want, track = panel.Fade(panel.Normal, bg, panel.StaleOpacity), panel.Fade(track, bg, panel.StaleOpacity)
				}
				for x := x0 + m.Bar/2; x <= x0+fill-2; x++ {
					if got := c.at(x, y); !near(got, want, 3) {
						t.Fatalf("%s %s: fill at %d is %v, want %v", where, id, x-x0, got, want)
					}
				}
				for x := x0 + fill + 1; x < x0+bar.W-m.Bar/2; x++ {
					if got := c.at(x, y); !near(got, track, 3) {
						t.Fatalf("%s %s: track at %d (fill %d) is %v, want %v", where, id, x-x0, fill, got, track)
					}
				}
			}
			// Codex's 5-hour: full, critical, its percent in red
			if fx.name == "combined" {
				rc := rects["r.codex.0"]
				bar := opOf(t, ops["r.codex.0"], panel.Pixels, "")
				y := int(rc.Top) + bar.Y + bar.H/2
				for x := int(rc.Left) + bar.X + m.Bar/2; x < int(rc.Left)+bar.X+bar.W-m.Bar/2; x++ {
					if got := c.at(x, y); !near(got, panel.Critical, 3) {
						t.Fatalf("%s: Codex 5-hour at %d is %v", where, x, got)
					}
				}
				pc := opOf(t, ops["r.codex.0"], panel.Text, "100%")
				red := 0
				for yy := int(rc.Top) + pc.Y; yy < int(rc.Top)+pc.Y+pc.H; yy++ {
					for xx := int(rc.Left) + pc.X; xx < int(rc.Left)+pc.X+pc.W; xx++ {
						if p := c.at(xx, yy); int(p.R) > int(p.G)+60 && int(p.R) > int(p.B)+60 {
							red++
						}
					}
				}
				if red == 0 {
					t.Fatalf("%s: no red pixel in the critical percent", where)
				}
			}
			// the header pictures: Clawd and the ring in #d97757 (faded when stale)
			for _, id := range []string{"h.claude", "h.codex"} {
				pic := opOf(t, ops[id], panel.Pixels, "")
				rc := rects[id]
				want := panel.Normal
				if stale && id == "h.claude" {
					want = panel.Fade(want, bg, panel.StaleOpacity)
				}
				n := 0
				for yy := int(rc.Top) + pic.Y; yy < int(rc.Top)+pic.Y+pic.H; yy++ {
					for xx := int(rc.Left) + pic.X; xx < int(rc.Left)+pic.X+pic.W; xx++ {
						if near(c.at(xx, yy), want, 3) {
							n++
						}
					}
				}
				if n < 12 {
					t.Fatalf("%s %s: %d pixels of the picture in %v", where, id, n, want)
				}
			}
			// States: the problem line under Codex's header, in this mode's colour
			if stale {
				rc := rects["h.codex"]
				pr := opOf(t, ops["h.codex"], panel.Text, tray.BuildPanel(fx.in).Sections[1].Problem)
				best := 999
				for yy := int(rc.Top) + pr.Y; yy < int(rc.Top)+pr.Y+pr.H; yy++ {
					for xx := int(rc.Left) + pr.X; xx < int(rc.Left)+pr.X+pr.W; xx++ {
						p, w := c.at(xx, yy), pal.Problem()
						d := max(abs(int(p.R)-int(w.R)), abs(int(p.G)-int(w.G)), abs(int(p.B)-int(w.B)))
						best = min(best, d)
					}
				}
				if best > 40 {
					t.Fatalf("%s: the problem line's colour is %d away from %v", where, best, pal.Problem())
				}
			}
			t.Logf("%s: %dx%d", where, c.w, c.h)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
