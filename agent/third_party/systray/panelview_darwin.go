//go:build !ios

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

/*
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include "panelview.h"
*/
import "C"

import (
	"errors"
	"image/color"
	"log"
	"sync"
	"unsafe"
)

var (
	viewerMu sync.Mutex
	viewer   PanelViewer
)

// SetPanelViewer sets what draws the items given a view with SetPanelView
// (nil: none; their views then draw nothing).
func SetPanelViewer(v PanelViewer) {
	viewerMu.Lock()
	defer viewerMu.Unlock()
	viewer = v
}

func currentViewer() PanelViewer {
	viewerMu.Lock()
	defer viewerMu.Unlock()
	return viewer
}

// SetPanelView gives the item a view that the PanelViewer draws (on:
// sized again and redrawn, unless the menu is open: an open menu keeps
// what it opened with, each view is sized again at the next open), or
// makes it a text item again (off).
func (item *MenuItem) SetPanelView(on bool) {
	C.set_menu_item_panel(C.int(item.id), C.bool(on))
}

// RenderPanelViewPNG draws the item's view alone, as the menu would, into
// a PNG: at least width points wide, at scale pixels per point, in the
// light or the dark appearance, over the window background colour (a
// stand-in for the menu's material). For tests: it must run on the main
// thread, and the item must be known to the PanelViewer.
func RenderPanelViewPNG(itemID uint32, width int, dark bool, scale int) ([]byte, error) {
	var n C.int
	p := C.aiu_render_png(C.int(itemID), C.int(width), C.bool(dark), C.int(scale), &n)
	if p == nil {
		return nil, errors.New("systray: the item's view could not be drawn")
	}
	defer C.free(p)
	return C.GoBytes(p, n), nil
}

// RaiseInNextPanelViewText makes the next text a view draws raise an
// Objective-C exception, which is caught and reported to the
// PanelViewer's ViewFailed as one in the drawing would be. For tests.
func RaiseInNextPanelViewText() { C.aiu_test_raise_once() }

// CancelPanelMenu cancels the open menu's tracking, on the main thread
// after the current call (nothing when no menu is open): the program calls
// it when its viewer stopped drawing, so that the open menu's views do not
// stay blank until it closes; the next open shows the program's new menu.
func CancelPanelMenu() { C.aiu_cancel_open_menu() }

// ActivateOnMenuOpen: before its menu shows, the program makes itself an
// accessory app and the active app (macOS gives an active app's menu the
// keys differently). Only for a CI build's test switch: a released
// program never calls it.
func ActivateOnMenuOpen(on bool) { C.aiu_set_activate(C.bool(on)) }

// viewCanvas measures with the menu's fonts and draws into the current
// graphics context, at scale pixels per point (the main thread only).
type viewCanvas struct{ scale int }

func (c viewCanvas) TextWidth(font int, s string) int {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return int(C.aiu_text_width(C.int(font), cs, C.int(c.scale)))
}

func (c viewCanvas) LineHeight(font int) int {
	return int(C.aiu_line_height(C.int(font), C.int(c.scale)))
}

func (c viewCanvas) Fill(x, y, w, h, radius int, col color.NRGBA) {
	C.aiu_fill(C.int(x), C.int(y), C.int(w), C.int(h), C.int(radius),
		C.uint8_t(col.R), C.uint8_t(col.G), C.uint8_t(col.B), C.uint8_t(col.A), C.int(c.scale))
}

func (c viewCanvas) Text(x, y, w, h int, s string, font int, col color.NRGBA, right, ellipsis bool) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.aiu_text(C.int(x), C.int(y), C.int(w), C.int(h), cs, C.int(font),
		C.uint8_t(col.R), C.uint8_t(col.G), C.uint8_t(col.B), C.uint8_t(col.A), C.bool(right), C.bool(ellipsis), C.int(c.scale))
}

func (c viewCanvas) Pixels(x, y, w, h int, bgra []byte, opacity uint8) {
	if w <= 0 || h <= 0 || len(bgra) < w*h*4 {
		return
	}
	C.aiu_pixels(C.int(x), C.int(y), C.int(w), C.int(h), (*C.uint8_t)(unsafe.Pointer(&bgra[0])), C.uint8_t(opacity), C.int(c.scale))
}

// survive logs a panic of the viewer instead of letting it end the
// program from inside AppKit (deferred in each callback).
func survive(what string) {
	if r := recover(); r != nil {
		log.Printf("systray error: the panel viewer panicked in %s: %v", what, r)
	}
}

//export systray_panel_view_size
func systray_panel_view_size(cID C.int, cW, cH *C.int) (ok C.bool) {
	defer survive("ViewSize")
	v := currentViewer()
	if v == nil {
		return false
	}
	w, h := v.ViewSize(uint32(cID), viewCanvas{scale: 1})
	*cW, *cH = C.int(w), C.int(h)
	return true
}

//export systray_panel_view_draw
func systray_panel_view_draw(cID, cW, cH, cScale C.int, cColours *C.uint8_t, cDark, cHighlighted C.bool) (ok C.bool) {
	defer survive("DrawView")
	v := currentViewer()
	if v == nil || cColours == nil {
		return false
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(cColours)), 16)
	at := func(i int) color.NRGBA { return color.NRGBA{R: b[i], G: b[i+1], B: b[i+2], A: b[i+3]} }
	cols := ViewColours{Dark: bool(cDark), Text: at(0), SecondaryText: at(4), Highlight: at(8), HighlightText: at(12)}
	v.DrawView(uint32(cID), viewCanvas{scale: int(cScale)}, int(cW), int(cH), int(cScale), cols, bool(cHighlighted))
	return true
}

//export systray_panel_view_label
func systray_panel_view_label(cID C.int) *C.char {
	defer survive("ViewLabel")
	v := currentViewer()
	if v == nil {
		return nil
	}
	return C.CString(v.ViewLabel(uint32(cID)))
}

//export systray_panel_menu_opening
func systray_panel_menu_opening() {
	defer survive("MenuOpening")
	if v := currentViewer(); v != nil {
		v.MenuOpening()
	}
}

//export systray_panel_menu_closed
func systray_panel_menu_closed() {
	defer survive("MenuClosed")
	if v := currentViewer(); v != nil {
		v.MenuClosed()
	}
}

//export systray_panel_view_failed
func systray_panel_view_failed(cWhat *C.char) {
	defer survive("ViewFailed")
	if v := currentViewer(); v != nil {
		v.ViewFailed(C.GoString(cWhat))
	}
}
