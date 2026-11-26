package trayhelper

import (
	"image/color"
	"log"
	"sync"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// The Windows usage panel (drawn-panel design 6.2, plan Task 8): every
// item of the tray menu is owner-drawn (stage 0: a menu holding one is
// drawn in the classic style, so the server line, the actions and the
// separators are drawn too, with one hover rule). Where things go is
// decided by package panel (panel.Size, panel.Layout); this file only
// carries the steps out with GDI.

var (
	pwUser32                     = windows.NewLazySystemDLL("user32.dll")
	pwGdi32                      = windows.NewLazySystemDLL("gdi32.dll")
	pwFillRect                   = pwUser32.NewProc("FillRect")
	pwDrawTextW                  = pwUser32.NewProc("DrawTextW")
	pwGetSysColor                = pwUser32.NewProc("GetSysColor")
	pwSystemParametersInfoW      = pwUser32.NewProc("SystemParametersInfoW")
	pwSystemParametersInfoForDpi = pwUser32.NewProc("SystemParametersInfoForDpi")
	pwCreateSolidBrush           = pwGdi32.NewProc("CreateSolidBrush")
	pwDeleteObject               = pwGdi32.NewProc("DeleteObject")
	pwSelectObject               = pwGdi32.NewProc("SelectObject")
	pwSetBkMode                  = pwGdi32.NewProc("SetBkMode")
	pwSetTextColor               = pwGdi32.NewProc("SetTextColor")
	pwCreateFontIndirectW        = pwGdi32.NewProc("CreateFontIndirectW")
	pwGetTextExtentPoint32W      = pwGdi32.NewProc("GetTextExtentPoint32W")
	pwGetTextMetricsW            = pwGdi32.NewProc("GetTextMetricsW")
	pwCreateCompatibleDC         = pwGdi32.NewProc("CreateCompatibleDC")
	pwDeleteDC                   = pwGdi32.NewProc("DeleteDC")
	pwCreateDIBSection           = pwGdi32.NewProc("CreateDIBSection")
	pwSaveDC                     = pwGdi32.NewProc("SaveDC")
	pwRestoreDC                  = pwGdi32.NewProc("RestoreDC")
	pwIntersectClipRect          = pwGdi32.NewProc("IntersectClipRect")
	pwEndMenu                    = pwUser32.NewProc("EndMenu")
	pwAlphaBlend                 = windows.NewLazySystemDLL("msimg32.dll").NewProc("AlphaBlend")
)

const (
	odsSelected = 0x0001

	dtRight       = 0x0002
	dtVCenter     = 0x0004
	dtSingleLine  = 0x0020
	dtCalcRect    = 0x0400
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000
)

// newItemDrawer is the Windows drawer, registered with the tray library.
func newItemDrawer() itemDrawer {
	d := newWinDrawer(loadMenuTheme)
	systray.SetMenuDrawer(d)
	return d
}

// winDrawer draws the panel's items: what each draws (drawStore, set by
// the helper's goroutine; what it held when the menu opened, while the
// menu is open: design 6.2, new values from the next open), the mode and
// the system's light colours (read at every open and at every settings
// change), the menu fonts per DPI. Its methods run on the tray's thread.
// After a panic in measuring or drawing it draws nothing more for the
// helper's life: the helper shows the text menu instead (Failed).
type winDrawer struct {
	drawStore
	load    func() (menuTheme, panel.Palette)
	endMenu func() // closes the menu that is open (EndMenu: the tray's thread's)

	mu     sync.Mutex
	theme  menuTheme
	light  panel.Palette
	fonts  map[uint32]*menuFonts
	open   map[uint32]panel.Item // what each item held when the menu opened
	failed bool
	// trace, when set (tests only), gets each Draw's steps
	trace func(id uint32, ops []panel.Op)
}

func newWinDrawer(load func() (menuTheme, panel.Palette)) *winDrawer {
	d := &winDrawer{load: load, endMenu: func() { pwEndMenu.Call() }, fonts: map[uint32]*menuFonts{}}
	d.theme, d.light = load()
	return d
}

// loadMenuTheme reads the mode (followDarkMode, AppsUseLightTheme, high
// contrast) and the system's menu colours.
func loadMenuTheme() (menuTheme, panel.Palette) {
	return themeFor(followDarkMode, appsUseLightTheme, highContrast), systemLightPalette()
}

func (d *winDrawer) palette() panel.Palette {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.theme.palette(d.light)
}

// DrawsActions is true: every item of the menu is drawn (stage 0).
func (d *winDrawer) DrawsActions() bool { return true }

// Failed reports whether a panic stopped the drawing (the helper then
// builds the text menu).
func (d *winDrawer) Failed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failed
}

// recovered ends a panic of what (deferred in each of the drawer's
// methods): logged once, the drawing stops and the open menu is closed.
func (d *winDrawer) recovered(what string) {
	r := recover()
	if r == nil {
		return
	}
	d.mu.Lock()
	first := !d.failed
	d.failed = true
	d.mu.Unlock()
	if first {
		log.Printf("panel: %s failed (%v); the tray menu is text from now on", what, r)
		d.endMenu()
	}
}

// reload reads the mode and the system's colours again; true when the
// colours changed.
func (d *winDrawer) reload() bool {
	th, light := d.load()
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := th.palette(light) != d.theme.palette(d.light)
	d.theme, d.light = th, light
	return changed
}

// MenuOpening: the menu is about to open. The mode is read again (a
// missed broadcast must not keep the wrong colours) and what each item
// holds now is what it draws until the next open.
func (d *winDrawer) MenuOpening() {
	defer d.recovered("opening the menu")
	if d.Failed() {
		return
	}
	d.reload()
	open := d.snapshot()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open = open
}

// SettingChanged follows a settings change: the mode and the colours are
// read again whatever the change (high contrast need not say
// "ImmersiveColorSet"), and a menu that is open when its colours change is
// closed (one colour set in a menu; the next open is in the new one); any
// setting may have changed the menu font, so the fonts are made again.
func (d *winDrawer) SettingChanged(area string) {
	defer d.recovered("a settings change")
	if d.Failed() {
		return
	}
	if d.reload() {
		d.endMenu()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, f := range d.fonts {
		f.close()
		delete(d.fonts, k)
	}
}

// item is what id draws: what it held when the menu opened, else what it
// holds now (an item added to the open menu).
func (d *winDrawer) item(id uint32) (panel.Item, bool) {
	d.mu.Lock()
	it, ok := d.open[id]
	d.mu.Unlock()
	if ok {
		return it, true
	}
	return d.Get(id)
}

// Clear forgets every item's data (the menu was rebuilt).
func (d *winDrawer) Clear() {
	d.drawStore.Clear()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open = nil
}

// Measure is the item's size at dpi (panel.Size with the menu font).
func (d *winDrawer) Measure(id, dpi uint32) (w, h int32) {
	m := panel.MetricsAt(int(dpi))
	w, h = int32(m.Width), int32(m.Item)
	defer d.recovered("measuring a menu item")
	if d.Failed() {
		return w, h
	}
	it, ok := d.item(id)
	if !ok {
		return w, h
	}
	dc, _, _ := pwCreateCompatibleDC.Call(0)
	if dc == 0 {
		return w, h
	}
	defer pwDeleteDC.Call(dc)
	sw, sh := panel.Size(it, m, d.measurer(dc, dpi))
	return int32(sw), int32(sh)
}

// Draw draws the item into rc (as wide as the menu: Windows adds a
// gutter to the measured width).
func (d *winDrawer) Draw(id uint32, hdc uintptr, rc systray.Rect, state, dpi uint32) {
	defer d.recovered("drawing a menu item")
	if d.Failed() {
		return
	}
	pal := d.palette()
	w, h := int(rc.Right-rc.Left), int(rc.Bottom-rc.Top)
	var ops []panel.Op
	if it, ok := d.item(id); ok {
		ops = panel.Layout(it, panel.MetricsAt(int(dpi)), d.measurer(hdc, dpi), w, h, pal, state&odsSelected != 0)
	} else {
		ops = []panel.Op{{Kind: panel.Fill, W: w, H: h, Colour: pal.Background}}
	}
	if d.trace != nil {
		d.trace(id, ops)
	}
	d.run(hdc, rc, dpi, ops)
}

// MeasureSeparator and DrawSeparator: the line between the groups.
func (d *winDrawer) MeasureSeparator(dpi uint32) (int32, int32) {
	w, h := panel.SeparatorSize(panel.MetricsAt(int(dpi)))
	return int32(w), int32(h)
}

func (d *winDrawer) DrawSeparator(hdc uintptr, rc systray.Rect, dpi uint32) {
	defer d.recovered("drawing a separator")
	if d.Failed() {
		return
	}
	m := panel.MetricsAt(int(dpi))
	d.run(hdc, rc, dpi, panel.SeparatorOps(m, int(rc.Right-rc.Left), int(rc.Bottom-rc.Top), d.palette()))
}

// MenuBackground fills the menu around its items in the panel's colour
// (the system's once the drawing stopped).
func (d *winDrawer) MenuBackground() (rgb uint32, ok bool) {
	defer d.recovered("the menu's background")
	if d.Failed() {
		return 0, false
	}
	return colorRef(d.palette().Background), true
}

// run carries the steps out in rc, clipped to it.
func (d *winDrawer) run(hdc uintptr, rc systray.Rect, dpi uint32, ops []panel.Op) {
	f := d.fontsFor(dpi)
	saved, _, _ := pwSaveDC.Call(hdc)
	defer pwRestoreDC.Call(hdc, saved)
	pwIntersectClipRect.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom))
	pwSetBkMode.Call(hdc, 1) // TRANSPARENT
	for _, o := range ops {
		r := systray.Rect{Left: rc.Left + int32(o.X), Top: rc.Top + int32(o.Y)}
		r.Right, r.Bottom = r.Left+int32(o.W), r.Top+int32(o.H)
		switch o.Kind {
		case panel.Fill:
			br, _, _ := pwCreateSolidBrush.Call(uintptr(colorRef(o.Colour)))
			if br != 0 {
				pwFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), br)
				pwDeleteObject.Call(br)
			}
		case panel.Text:
			s, err := windows.UTF16FromString(o.Text)
			if err != nil {
				continue
			}
			pwSelectObject.Call(hdc, f.get(o.Font))
			pwSetTextColor.Call(hdc, uintptr(colorRef(o.Colour)))
			flags := uintptr(dtSingleLine | dtVCenter | dtNoPrefix)
			if o.Align == panel.Right {
				flags |= dtRight
			}
			if o.Ellipsis {
				flags |= dtEndEllipsis
			}
			pwDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&s[0])), uintptr(len(s)-1), uintptr(unsafe.Pointer(&r)), flags)
		case panel.Pixels:
			alphaBlend(hdc, int(r.Left), int(r.Top), o.W, o.H, o.Pixels, o.Opacity)
		}
	}
}

// alphaBlend puts a premultiplied BGRA picture at (x, y) through a 32-bit
// top-down DIB (msimg32!AlphaBlend, AC_SRC_ALPHA).
func alphaBlend(hdc uintptr, x, y, w, h int, px []byte, opacity uint8) {
	if w <= 0 || h <= 0 || len(px) != w*h*4 {
		return
	}
	mem, _, _ := pwCreateCompatibleDC.Call(hdc)
	if mem == 0 {
		return
	}
	defer pwDeleteDC.Call(mem)
	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	bmp, _, _ := pwCreateDIBSection.Call(mem, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return
	}
	defer pwDeleteObject.Call(bmp) // also when there are no bits
	if bits == nil {
		return
	}
	copy(unsafe.Slice((*byte)(bits), len(px)), px)
	old, _, _ := pwSelectObject.Call(mem, bmp)
	defer pwSelectObject.Call(mem, old)
	const acSrcOver, acSrcAlpha = 0, 1
	blend := uintptr(acSrcOver) | uintptr(opacity)<<16 | uintptr(acSrcAlpha)<<24
	pwAlphaBlend.Call(hdc, uintptr(x), uintptr(y), uintptr(w), uintptr(h), mem, 0, 0, uintptr(w), uintptr(h), blend)
}

type bitmapInfoHeader struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}

func colorRef(c color.NRGBA) uint32 { return uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16 }

// menuFonts are the menu font and its bold and semi-bold faces at one DPI.
type menuFonts struct{ regular, bold, semiBold uintptr }

func (f *menuFonts) get(font panel.Font) uintptr {
	switch font {
	case panel.Bold:
		return f.bold
	case panel.SemiBold:
		return f.semiBold
	}
	return f.regular
}

func (f *menuFonts) close() {
	for _, h := range []uintptr{f.regular, f.bold, f.semiBold} {
		if h != 0 {
			pwDeleteObject.Call(h)
		}
	}
}

// fontsFor are the menu fonts at dpi, made once.
func (d *winDrawer) fontsFor(dpi uint32) *menuFonts {
	d.mu.Lock()
	defer d.mu.Unlock() // deferred: a panic here must not leave it locked
	return d.fontsAt(dpi)
}

// fontsAt is fontsFor with d.mu held.
func (d *winDrawer) fontsAt(dpi uint32) *menuFonts {
	if f, ok := d.fonts[dpi]; ok {
		return f
	}
	lf := menuLogFont(dpi)
	f := &menuFonts{}
	f.regular, _, _ = pwCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	lf.Weight = 700 // FW_BOLD
	f.bold, _, _ = pwCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	lf.Weight = 600 // FW_SEMIBOLD
	f.semiBold, _, _ = pwCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	d.fonts[dpi] = f
	return f
}

type logFont struct {
	Height, Width, Escapement, Orientation, Weight int32
	Italic, Underline, StrikeOut, CharSet          byte
	OutPrecision, ClipPrecision, Quality, Pitch    byte
	FaceName                                       [32]uint16
}

type nonClientMetrics struct {
	Size                                                              uint32
	BorderWidth, ScrollWidth, ScrollHeight, CaptionWidth, CaptionHght int32
	CaptionFont                                                       logFont
	SmCaptionWidth, SmCaptionHeight                                   int32
	SmCaptionFont                                                     logFont
	MenuWidth, MenuHeight                                             int32
	MenuFont, StatusFont, MessageFont                                 logFont
	PaddedBorderWidth                                                 int32
}

// menuLogFont is the menu font at dpi (SystemParametersInfoForDpi; older
// Windows: the system's, scaled from 96 DPI); Segoe UI 9 pt if neither
// answers.
func menuLogFont(dpi uint32) logFont {
	const spiGetNonClientMetrics = 0x0029
	var ncm nonClientMetrics
	ncm.Size = uint32(unsafe.Sizeof(ncm))
	if pwSystemParametersInfoForDpi.Find() == nil {
		if r, _, _ := pwSystemParametersInfoForDpi.Call(spiGetNonClientMetrics, uintptr(ncm.Size), uintptr(unsafe.Pointer(&ncm)), 0, uintptr(dpi)); r != 0 {
			return ncm.MenuFont
		}
	}
	if r, _, _ := pwSystemParametersInfoW.Call(spiGetNonClientMetrics, uintptr(ncm.Size), uintptr(unsafe.Pointer(&ncm)), 0); r != 0 {
		lf := ncm.MenuFont
		lf.Height = int32(int64(lf.Height) * int64(dpi) / 96)
		return lf
	}
	lf := logFont{Height: -int32((12*dpi + 48) / 96), Weight: 400}
	copy(lf.FaceName[:], windows.StringToUTF16("Segoe UI"))
	return lf
}

// measurer measures with the menu fonts at dpi in dc.
func (d *winDrawer) measurer(dc uintptr, dpi uint32) gdiMeasurer {
	return gdiMeasurer{dc: dc, f: d.fontsFor(dpi)}
}

type gdiMeasurer struct {
	dc uintptr
	f  *menuFonts
}

func (g gdiMeasurer) TextWidth(font panel.Font, s string) int {
	u, err := windows.UTF16FromString(s)
	if err != nil || len(u) <= 1 {
		return 0
	}
	old, _, _ := pwSelectObject.Call(g.dc, g.f.get(font))
	defer pwSelectObject.Call(g.dc, old)
	var size struct{ CX, CY int32 }
	pwGetTextExtentPoint32W.Call(g.dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&size)))
	return int(size.CX)
}

func (g gdiMeasurer) LineHeight(font panel.Font) int {
	old, _, _ := pwSelectObject.Call(g.dc, g.f.get(font))
	defer pwSelectObject.Call(g.dc, old)
	var tm [16]int32 // TEXTMETRICW (60 bytes); tmHeight is its first LONG
	pwGetTextMetricsW.Call(g.dc, uintptr(unsafe.Pointer(&tm[0])))
	return int(tm[0])
}

// systemLightPalette is the light panel in the system's menu colours
// (COLOR_MENU, COLOR_MENUTEXT, COLOR_HIGHLIGHT, COLOR_HIGHLIGHTTEXT): what
// Windows draws a classic menu with, high contrast included.
func systemLightPalette() panel.Palette {
	sys := func(i uintptr) color.NRGBA {
		c, _, _ := pwGetSysColor.Call(i)
		return color.NRGBA{uint8(c), uint8(c >> 8), uint8(c >> 16), 0xff}
	}
	return panel.Palette{Background: sys(4), Text: sys(7), Highlight: sys(13), HighlightText: sys(14)}
}
