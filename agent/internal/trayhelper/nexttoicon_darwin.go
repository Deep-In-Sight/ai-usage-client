//go:build cgo

package trayhelper

import "fyne.io/systray"

// setNextToIcon sets the menu bar title.
func setNextToIcon(t string) { systray.SetTitle(t) }
