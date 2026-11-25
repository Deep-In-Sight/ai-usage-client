package trayhelper

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel/panelcheck"
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

// testDrawer is a drawer whose Draw steps are kept (by id) and whose
// EndMenu is counted.
type testDrawer struct {
	*winDrawer
	mu   sync.Mutex
	ops  map[uint32][]panel.Op
	ends int
}

func newTestDrawer(load func() (menuTheme, panel.Palette)) *testDrawer {
	t := &testDrawer{winDrawer: newWinDrawer(load), ops: map[uint32][]panel.Op{}}
	t.trace = func(id uint32, ops []panel.Op) { t.mu.Lock(); t.ops[id] = ops; t.mu.Unlock() }
	t.endMenu = func() { t.mu.Lock(); t.ends++; t.mu.Unlock() }
	return t
}

func (t *testDrawer) drawn(id uint32) []panel.Op {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ops[id]
}

func (t *testDrawer) ended() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ends
}

// longestItems are the longest texts a real menu gets: the longest
// account with the PCs and a plan, the server line naming two fetching
// PCs with Windows' default host names.
var longestItems = map[string]panel.Item{
	"long account": {Kind: "header", Provider: "codex", Name: "Codex", Account: "firstname.lastname.…@… · 12 PCs · Enterprise", Updated: "Updated Mon 15:30"},
	"two hosts":    {Kind: "info", Text: "Server connected · Claude from DESKTOP-ABCDEFG · Codex from DESKTOP-HIJKLMN", Dim: true},
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
// at 100, 125, 150 and 200 %, with the menu font: every text of every item
// drawn whole at the measured size (review B-I1: a step Layout had to
// clamp fails), also in an item wider than measured (Windows widens it);
// the fixture's texts fit the panel's width; every item but the longest
// account measures the panel's width, the server line naming two
// fetching PCs too (it wraps, review A-I2); the longest account widens
// its header instead of being cut (review A-I1).
func TestMeasureMatchesDrawAtEveryDPI(t *testing.T) {
	comb := panel.Items(tray.BuildPanel(panelfixture.Combined()))
	states := panel.Items(tray.BuildPanel(panelfixture.States()))
	items := map[string]panel.Item{
		"more":   {Kind: "more", Label: "More limits on the dashboard"},
		"action": {Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true},
	}
	for id, it := range comb {
		items["combined "+id] = it
	}
	for id, it := range states {
		items["states "+id] = it
	}
	for _, in := range []tray.MenuInputs{panelfixture.Combined(), panelfixture.States()} {
		p := tray.BuildPanel(in)
		items["server "+p.Server] = panel.Item{Kind: "info", Text: p.Server, Dim: true}
		for _, a := range p.Actions {
			items["action "+a.ID] = panel.Item{Kind: "action", Text: a.Text, Enabled: a.Enabled, Checkbox: a.Checkbox, Checked: a.Checked}
		}
	}
	for k, it := range longestItems {
		items[k] = it
	}
	for _, dpi := range []uint32{96, 120, 144, 192} {
		m := panel.MetricsAt(int(dpi))
		d := newTestDrawer(fixed(menuTheme{}, panel.LightPalette))
		c := newCanvas(t, 3*m.Width, 4*m.Row)
		tm := d.measurer(c.dc, dpi)
		for name, it := range items {
			d.Set(1, &it)
			w, h := d.Measure(1, dpi)
			sw, sh := panel.Size(it, m, tm)
			if int(w) != sw || int(h) != sh {
				t.Fatalf("dpi %d %s: measured %dx%d, layout %dx%d", dpi, name, w, h, sw, sh)
			}
			switch {
			case name == "long account":
				need := m.Pad + m.Picture + 3*((8*m.Pad+8)/16) + m.Pad + calcWidth(t, d.winDrawer, c.dc, dpi, panel.Bold, it.Name) +
					calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, it.Account) + calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, it.Updated)
				if int(w) < need {
					t.Fatalf("dpi %d: the longest account's header is %d wide, its line needs %d", dpi, w, need)
				}
			case int(w) != m.Width:
				t.Fatalf("dpi %d %s: %d wide, the panel is %d", dpi, name, w, m.Width)
			}
			for _, gutter := range []int32{0, int32(dpi * 3 / 10)} { // Windows widens the item (stage 0: 36 px at 120 DPI)
				for _, state := range []uint32{0, odsSelected} {
					d.Draw(1, c.dc, systray.Rect{Right: w + gutter, Bottom: h}, state, dpi)
					if miss := panelcheck.Missing(it, d.drawn(1), m, tm, int(w+gutter), int(h)); len(miss) > 0 {
						t.Fatalf("dpi %d %s (%dx%d): %s", dpi, name, w+gutter, h, strings.Join(miss, "; "))
					}
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
				line := m.Picture + gap + calcWidth(t, d.winDrawer, c.dc, dpi, panel.Bold, s.Name) + gap +
					calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, s.Account) + gap + calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, s.Updated)
				if line > inner {
					t.Fatalf("dpi %d: %s's header is %d px, the panel has %d", dpi, s.Name, line, inner)
				}
				for _, l := range panel.Wrap(s.Problem, panel.Regular, inner, tm) {
					if w := calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, l); w > inner {
						t.Fatalf("dpi %d: problem line %q is %d px", dpi, l, w)
					}
				}
				for _, r := range s.Rows {
					if w := calcWidth(t, d.winDrawer, c.dc, dpi, panel.SemiBold, r.Label) + gap + calcWidth(t, d.winDrawer, c.dc, dpi, panel.Bold, r.PercentText); w > inner {
						t.Fatalf("dpi %d: row %q is %d px", dpi, r.Label, w)
					}
					if w := calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, r.Detail); w > inner {
						t.Fatalf("dpi %d: %q is %d px", dpi, r.Detail, w)
					}
				}
			}
			for _, a := range p.Actions {
				if w := m.Check + calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, a.Text); w > inner {
					t.Fatalf("dpi %d: %q is %d px", dpi, a.Text, w)
				}
			}
		}
		// the server line with two hosts: more than one line, each within
		// the panel's text width
		lines := panel.Wrap(longestItems["two hosts"].Text, panel.Regular, inner, tm)
		if len(lines) < 2 {
			t.Fatalf("dpi %d: the two-host server line is one line", dpi)
		}
		for _, l := range lines {
			if w := calcWidth(t, d.winDrawer, c.dc, dpi, panel.Regular, l); w > inner {
				t.Fatalf("dpi %d: server line %q is %d px", dpi, l, w)
			}
		}
	}
}

// RF5 with the one-switch ruling: the drawing is in the dark set only in
// dark mode, in the system's menu colours otherwise; the system's colours
// are those GetSysColor gives, read here on its own (review B-M11), and
// what the runner reads is what its registry and high-contrast setting say.
func TestDrawsTheModesColours(t *testing.T) {
	it := panel.Items(tray.BuildPanel(panelfixture.Combined()))["h.claude"]
	sysColor := windows.NewLazySystemDLL("user32.dll").NewProc("GetSysColor")
	sys := func(i uintptr) color.NRGBA {
		c, _, _ := sysColor.Call(i)
		return color.NRGBA{uint8(c), uint8(c >> 8), uint8(c >> 16), 0xff}
	}
	light := systemLightPalette()
	eq(t, light, panel.Palette{Background: sys(4), Text: sys(7), Highlight: sys(13), HighlightText: sys(14)})
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
	// the runner's own reading
	th, pal := loadMenuTheme()
	eq(t, pal, light)
	wantDark := false
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetIntegerValue("AppsUseLightTheme")
		wantDark = err == nil && v == 0 && !highContrast()
		k.Close()
	}
	eq(t, th.Dark, wantDark)
	t.Logf("this runner: dark %v, light palette %+v (menu %v)", th.Dark, pal, pal.Background)
}

// RF5 and review A-m2, A-m3: the mode is read again at every settings
// change, whatever its text, and at every open; a change of the colours
// closes an open menu (once per change), dark to light as well as light to
// dark (review B-M5); a change that leaves the colours alone closes
// nothing.
func TestThemeChangeIsFollowed(t *testing.T) {
	loads, dark := 0, false
	d := newTestDrawer(func() (menuTheme, panel.Palette) {
		loads++
		return menuTheme{Dark: dark}, panel.LightPalette
	})
	eq(t, loads, 1)
	for _, area := range []string{"WindowMetrics", "", "Policy"} {
		d.SettingChanged(area)
	}
	eq(t, loads, 4)
	eq(t, d.ended(), 0)
	dark = true
	d.SettingChanged("WindowMetrics") // high contrast or a policy may come so
	eq(t, loads, 5)
	eq(t, d.ended(), 1)
	rgb, _ := d.MenuBackground()
	eq(t, rgb, colorRef(panel.DarkPalette.Background))
	d.SettingChanged("ImmersiveColorSet") // no change any more
	eq(t, d.ended(), 1)
	it := panel.Item{Kind: "action", Text: "Quit", Enabled: true}
	d.Set(3, &it)
	cv := newCanvas(t, 400, 40)
	d.Draw(3, cv.dc, systray.Rect{Right: 400, Bottom: 30}, 0, 96)
	eq(t, cv.at(398, 1), panel.DarkPalette.Background)
	// dark to light
	dark = false
	d.SettingChanged("ImmersiveColorSet")
	eq(t, d.ended(), 2)
	d.Draw(3, cv.dc, systray.Rect{Right: 400, Bottom: 30}, 0, 96)
	eq(t, cv.at(398, 1), panel.LightPalette.Background)
	// a broadcast that never came: the next open reads the mode
	dark = true
	n := loads
	d.MenuOpening()
	eq(t, loads, n+1)
	rgb, _ = d.MenuBackground()
	eq(t, rgb, colorRef(panel.DarkPalette.Background))
	eq(t, d.ended(), 2) // nothing is open yet
}

// Review A-m14: an open menu keeps the values it opened with; a new view
// shows from the next open (design 6.2). An item added to the open menu
// draws what it holds.
func TestOpenMenuKeepsItsValues(t *testing.T) {
	d := newTestDrawer(fixed(menuTheme{}, panel.LightPalette))
	a := panel.Item{Kind: "action", Text: "Refresh now", Enabled: true}
	b := panel.Item{Kind: "action", Text: "Refreshing…"}
	d.Set(1, &a)
	d.MenuOpening()
	d.Set(1, &b)
	cv := newCanvas(t, 400, 40)
	text := func() string {
		d.Draw(1, cv.dc, systray.Rect{Right: 400, Bottom: 30}, 0, 96)
		for _, o := range d.drawn(1) {
			if o.Kind == panel.Text {
				return o.Text
			}
		}
		return ""
	}
	eq(t, text(), "Refresh now")
	if w, h := d.Measure(1, 96); w != 320 || h != 30 {
		t.Fatalf("measured %dx%d", w, h)
	}
	d.Set(2, &b) // added while open
	d.Draw(2, cv.dc, systray.Rect{Right: 400, Bottom: 30}, 0, 96)
	if ops := d.drawn(2); len(ops) < 2 || ops[1].Text != "Refreshing…" {
		t.Fatalf("an added item draws %+v", ops)
	}
	d.MenuOpening()
	eq(t, text(), "Refreshing…")
	// a rebuilt menu forgets every item (review A-m6)
	d.Clear()
	if _, ok := d.item(1); ok {
		t.Fatal("item 1 kept after Clear")
	}
}

// Review A-m4, A-m5: a panic in measuring or drawing is logged once; the
// drawer draws nothing more (the helper shows the text menu: Failed), the
// open menu is closed, the menu's background is the system's again, and
// nothing deadlocks (the drawer's lock is released by a deferred unlock).
func TestDrawerPanicStopsTheDrawing(t *testing.T) {
	var buf strings.Builder
	var lmu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { lmu.Lock(); defer lmu.Unlock(); return buf.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	d := newTestDrawer(fixed(menuTheme{}, panel.LightPalette))
	d.fonts = nil // the first font made panics (a nil map): a drawer bug
	it := panel.Item{Kind: "action", Text: "Quit", Enabled: true}
	d.Set(1, &it)
	done := make(chan [2]int32, 1)
	cv := newCanvas(t, 400, 40)
	go func() {
		w, h := d.Measure(1, 96)
		w2, h2 := d.Measure(1, 120) // stopped: no second panic, no deadlock
		d.Draw(1, cv.dc, systray.Rect{Right: 400, Bottom: 30}, 0, 96)
		d.DrawSeparator(cv.dc, systray.Rect{Right: 400, Bottom: 13}, 96)
		d.SettingChanged("ImmersiveColorSet")
		d.MenuOpening()
		if w2 != 400 || h2 != 38 {
			w = -1
		}
		done <- [2]int32{w, h}
	}()
	select {
	case got := <-done:
		eq(t, got, [2]int32{320, 30}) // the panel's default size, not 0×0
	case <-time.After(5 * time.Second):
		t.Fatal("the drawer deadlocked after a panic")
	}
	eq(t, d.Failed(), true)
	eq(t, d.ended(), 1)
	if ops := d.drawn(1); ops != nil {
		t.Fatalf("drawn after the panic: %+v", ops)
	}
	if _, ok := d.MenuBackground(); ok {
		t.Fatal("the menu keeps the drawer's background")
	}
	ui := systrayUI{drawer: d}
	eq(t, ui.Draws(), false)
	lmu.Lock()
	got := buf.String()
	lmu.Unlock()
	if n := strings.Count(got, "the tray menu is text from now on"); n != 1 {
		t.Fatalf("logged %d times: %q", n, got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Review A-m6: a rebuild (Reset) makes the drawer forget the old items.
func TestResetForgetsTheDrawnItems(t *testing.T) {
	d := newWinDrawer(fixed(menuTheme{}, panel.LightPalette))
	it := panel.Item{Kind: "action", Text: "Quit", Enabled: true}
	d.Set(7, &it)
	systrayUI{drawer: d}.Reset()
	if _, ok := d.Get(7); ok {
		t.Fatal("item 7 kept after Reset")
	}
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
	d := newTestDrawer(fixed(th, light))
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
			ops[e.id] = d.drawn(ids[e.id])
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
				// the percent's pixels nearer the critical colour than the
				// normal one, the text's and the background (review B-M6:
				// the normal orange passed "red" before)
				pc := opOf(t, ops["r.codex.0"], panel.Text, "100%")
				dist := func(a, b color.NRGBA) int {
					return abs(int(a.R)-int(b.R)) + abs(int(a.G)-int(b.G)) + abs(int(a.B)-int(b.B))
				}
				red := 0
				for yy := int(rc.Top) + pc.Y; yy < int(rc.Top)+pc.Y+pc.H; yy++ {
					for xx := int(rc.Left) + pc.X; xx < int(rc.Left)+pc.X+pc.W; xx++ {
						p := c.at(xx, yy)
						if dc := dist(p, panel.Critical); dc < dist(p, panel.Normal) && dc < dist(p, pal.Text) && dc < dist(p, bg) {
							red++
						}
					}
				}
				if red < 8 {
					t.Fatalf("%s: %d pixels of the critical percent are in its colour", where, red)
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
