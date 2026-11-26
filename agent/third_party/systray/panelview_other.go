//go:build !darwin

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

import "errors"

// SetPanelViewer does nothing here: menu items with a view are macOS'.
func SetPanelViewer(PanelViewer) {}

// SetPanelView does nothing here: menu items with a view are macOS'.
func (item *MenuItem) SetPanelView(bool) {}

// RenderPanelViewPNG is macOS' (a test hook).
func RenderPanelViewPNG(uint32, int, bool, int) ([]byte, error) {
	return nil, errors.New("systray: menu item views are macOS'")
}

// RaiseInNextPanelViewText does nothing here (a test hook of macOS').
func RaiseInNextPanelViewText() {}

// ActivateOnMenuOpen does nothing here (a CI test switch of macOS').
func ActivateOnMenuOpen(bool) {}
