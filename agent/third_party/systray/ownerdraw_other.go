//go:build !windows

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

// SetMenuDrawer does nothing here: owner-drawn menu items are Windows'.
func SetMenuDrawer(MenuDrawer) {}

// SetOwnerDrawn does nothing here: owner-drawn menu items are Windows'.
func (item *MenuItem) SetOwnerDrawn(bool) {}

// MenuOwnerWindow is 0 here: the hidden window that owns the menu is
// Windows'.
func MenuOwnerWindow() uintptr { return 0 }

// AddOwnerDrawnSeparator is AddSeparator here.
func AddOwnerDrawnSeparator() { AddSeparator() }
