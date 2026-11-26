//go:build darwin && cgo

package trayhelper

import "fyne.io/systray"

// newItemDrawer is the macOS drawer: the tray library's menu item views
// (systray.SetPanelView) ask it what to draw (panelview.go).
func newItemDrawer() itemDrawer {
	d := newViewDrawer()
	systray.SetPanelViewer(viewerGlue{d})
	return d
}

// viewerGlue hands the tray library's calls to the drawer (the library's
// types are only in builds with cgo).
type viewerGlue struct{ d *viewDrawer }

func (g viewerGlue) ViewSize(id uint32, c systray.ViewCanvas) (int, int) { return g.d.ViewSize(id, c) }
func (g viewerGlue) ViewLabel(id uint32) string                          { return g.d.ViewLabel(id) }
func (g viewerGlue) MenuOpening()                                        { g.d.MenuOpening() }
func (g viewerGlue) MenuClosed()                                         { g.d.MenuClosed() }
func (g viewerGlue) ViewFailed(what string)                              { g.d.ViewFailed(what) }

func (g viewerGlue) DrawView(id uint32, c systray.ViewCanvas, w, h, scale int, cols systray.ViewColours, highlighted bool) {
	g.d.DrawView(id, c, w, h, scale, viewColours(cols), highlighted)
}

func activateOnMenuOpen() { systray.ActivateOnMenuOpen(true) }
