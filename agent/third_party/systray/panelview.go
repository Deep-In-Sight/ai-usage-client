// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

import "image/color"

// ViewCanvas is what a PanelViewer measures with and draws on (macOS):
// the menu's fonts (0 the menu font, 1 its bold face, 2 its semi-bold
// face) and the item's view, in pixels at the canvas's scale (pixels per
// point; a canvas for measuring an item's size has scale 1, so its sizes
// are points). Sizes are rounded up.
type ViewCanvas interface {
	TextWidth(font int, s string) int
	LineHeight(font int) int
	// Fill fills a rectangle, its corners rounded by radius.
	Fill(x, y, w, h, radius int, c color.NRGBA)
	// Text draws s in one line, vertically centred in its box, at its
	// left or right; ellipsis: a text longer than the box ends in "…".
	Text(x, y, w, h int, s string, font int, c color.NRGBA, right, ellipsis bool)
	// Pixels draws a w×h picture of premultiplied blue, green, red,
	// alpha bytes (the top row first) at opacity.
	Pixels(x, y, w, h int, bgra []byte, opacity uint8)
}

// ViewColours are the menu's colours where an item is drawn, for the
// appearance it is drawn in (macOS: labelColor, secondaryLabelColor,
// selectedContentBackgroundColor and selectedMenuItemTextColor, in sRGB).
type ViewColours struct {
	Dark                                         bool
	Text, SecondaryText, Highlight, HighlightText color.NRGBA
}

// PanelViewer draws the menu items that SetPanelView gave a view (macOS;
// elsewhere it is never called). Its methods run on the main thread,
// ViewSize and DrawView while AppKit lays the menu out or draws it: they
// must not block, nor wait for anything that waits for the main thread.
type PanelViewer interface {
	// ViewSize is the item's size in points (c's scale is 1). The view
	// is at least that wide and as wide as the menu.
	ViewSize(itemID uint32, c ViewCanvas) (width, height int)
	// DrawView draws the item, width×height pixels at scale pixels per
	// point; highlighted: the item is enabled and under the pointer or
	// chosen with the keyboard.
	DrawView(itemID uint32, c ViewCanvas, width, height, scale int, colours ViewColours, highlighted bool)
	// ViewLabel is what VoiceOver says for the item.
	ViewLabel(itemID uint32) string
	// MenuOpening: the menu is about to open (each view is then sized
	// again); MenuClosed: it closed.
	MenuOpening()
	MenuClosed()
}
