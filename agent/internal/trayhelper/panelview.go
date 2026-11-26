package trayhelper

import (
	"image/color"
	"log"
	"sync"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// The macOS usage panel (drawn-panel design 6.3, plan Task 9): the
// panel's headers, rows and its lines that cannot be clicked (the server
// line, the sign-in prompt) are menu items with a view that the tray
// library's Objective-C draws; the actions stay the menu's own text items
// (their look, check mark, keyboard and VoiceOver names). Where everything
// goes, its size and the panel's colours are decided by package panel
// (panel.Size, panel.Layout) exactly as on Windows; the view only carries
// the steps out, in the system's label colours for its appearance.
//
// This file is plain Go, built and tested on every OS; the tray library's
// calls (systray.PanelViewer) reach it through panelview_darwin.go.

// viewCanvas is what the view measures with and draws on: the menu's
// fonts (panel.Font as a number) in pixels at the view's scale
// (systray.ViewCanvas).
type viewCanvas interface {
	TextWidth(font int, s string) int
	LineHeight(font int) int
	Fill(x, y, w, h, radius int, c color.NRGBA)
	Text(x, y, w, h int, s string, font int, c color.NRGBA, right, ellipsis bool)
	Pixels(x, y, w, h int, bgra []byte, opacity uint8)
}

// viewColours are the menu's colours for the view's appearance
// (systray.ViewColours).
type viewColours struct {
	Dark                                          bool
	Text, SecondaryText, Highlight, HighlightText color.NRGBA
}

// The macOS selection is a rounded fill inset from the item's sides, as
// the menu highlights its own items (points): measured on CI against the
// menu's own highlight of a text item (macOS 26: inset 5 from the sides,
// the item's full height, corners of about 6; CI panel.mac_menu compares
// the two).
const (
	macHighlightInset  = 5
	macHighlightRadius = 6
)

// macPalette is the panel's palette over macOS' menu: no background of the
// panel's own (the menu's material shows through), the system's label and
// secondary label colours, the selection's colours.
func macPalette(c viewColours) panel.Palette {
	return panel.Palette{Dark: c.Dark, Text: c.Text, Dimmed: c.SecondaryText, Highlight: c.Highlight, HighlightText: c.HighlightText,
		HighlightInset: macHighlightInset, HighlightRadius: macHighlightRadius}
}

// canvasMeasurer measures with a view canvas (panel.Measurer).
type canvasMeasurer struct{ c viewCanvas }

func (m canvasMeasurer) TextWidth(f panel.Font, s string) int { return m.c.TextWidth(int(f), s) }
func (m canvasMeasurer) LineHeight(f panel.Font) int          { return m.c.LineHeight(int(f)) }

// viewDrawer keeps what each view item draws, by its menu id (drawStore,
// set by the helper's goroutine), and answers the tray library's calls on
// the main thread. While the menu is open every view draws what it held
// when the menu opened (one panel, never half old and half new; the next
// open shows the new values, as on Windows); a view added to the open
// menu draws what it holds now. After a panic it draws nothing more for
// the helper's life: the helper shows the text menu instead (Failed).
type viewDrawer struct {
	drawStore

	mu     sync.Mutex
	isOpen bool
	open   map[uint32]panel.Item
	failed bool
	// trace, when set (tests only), gets each DrawView's steps and the
	// measurer they were laid out with
	trace func(id uint32, ops []panel.Op, m panel.Metrics, tm panel.Measurer, w, h int)
}

func newViewDrawer() *viewDrawer { return &viewDrawer{} }

// DrawsActions is false: the actions stay the menu's own items.
func (*viewDrawer) DrawsActions() bool { return false }

// Failed reports whether a panic stopped the drawing.
func (d *viewDrawer) Failed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failed
}

// recovered ends a panic of what (deferred in each method the tray
// library calls): logged once, and nothing is drawn from then on.
func (d *viewDrawer) recovered(what string) {
	if r := recover(); r != nil {
		d.fail(what, r)
	}
}

// fail stops the drawing for good, logged once.
func (d *viewDrawer) fail(what string, why any) {
	d.mu.Lock()
	first := !d.failed
	d.failed = true
	d.mu.Unlock()
	if first {
		log.Printf("panel: %s failed (%v); the tray menu is text from now on", what, why)
	}
}

// ViewFailed: the tray library caught an Objective-C exception while a
// view was measured or drawn (review A3-M10); the drawing stops as after
// a panic.
func (d *viewDrawer) ViewFailed(what string) {
	d.fail("drawing (Objective-C)", what)
}

// item is what id draws now: what it held when the open menu opened, else
// what it holds.
func (d *viewDrawer) item(id uint32) (panel.Item, bool) {
	d.mu.Lock()
	it, ok := d.open[id]
	d.mu.Unlock()
	if ok {
		return it, true
	}
	return d.Get(id)
}

// Clear forgets every item (the menu was rebuilt).
func (d *viewDrawer) Clear() {
	d.drawStore.Clear()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open = nil
}

// MenuOpening: what each item holds now is what it draws until the menu
// closes.
func (d *viewDrawer) MenuOpening() {
	defer d.recovered("opening the menu")
	open := d.snapshot()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.isOpen, d.open = true, open
}

// MenuClosed: views draw what they hold again.
func (d *viewDrawer) MenuClosed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.isOpen, d.open = false, nil
}

// ViewSize is the item's size in points: panel.Size at 96 DPI (a point
// is a pixel at 96 DPI there) with the menu's fonts; after a failure, or
// for an id it does not know, the size of a text item.
func (d *viewDrawer) ViewSize(id uint32, c viewCanvas) (w, h int) {
	m := panel.MetricsAt(96)
	w, h = m.Width, m.Item
	defer d.recovered("measuring")
	if d.Failed() {
		return w, h
	}
	it, ok := d.item(id)
	if !ok {
		return w, h
	}
	return panel.Size(it, m, canvasMeasurer{c})
}

// DrawView draws the item w×h pixels at scale pixels per point: the
// panel's layout at 96×scale DPI (sizes in points × scale exactly, the
// text measured at the menu's point size × scale, so the lines wrap as
// they did when the view was sized), carried out on c.
func (d *viewDrawer) DrawView(id uint32, c viewCanvas, w, h, scale int, cols viewColours, highlighted bool) {
	defer d.recovered("drawing")
	if d.Failed() {
		return
	}
	it, ok := d.item(id)
	if !ok {
		return
	}
	scale = min(max(scale, 1), 3)
	m, tm := panel.MetricsAt(96*scale), canvasMeasurer{c}
	ops := panel.Layout(it, m, tm, w, h, macPalette(cols), highlighted)
	if d.trace != nil {
		d.trace(id, ops, m, tm, w, h)
	}
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
}

// ViewLabel is what VoiceOver says for the item (panel.AccessibleText).
func (d *viewDrawer) ViewLabel(id uint32) string {
	defer d.recovered("naming an item")
	it, ok := d.item(id)
	if !ok {
		return ""
	}
	return panel.AccessibleText(it)
}
