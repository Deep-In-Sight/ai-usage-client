//go:build !windows && !(darwin && cgo)

package trayhelper

// newItemDrawer: only Windows and macOS (with cgo: its tray) draw the
// panel's items themselves.
func newItemDrawer() itemDrawer { return nil }
