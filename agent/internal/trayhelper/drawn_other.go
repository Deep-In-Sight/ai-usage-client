//go:build !windows

package trayhelper

// newItemDrawer: only Windows draws the panel's items itself (so far).
func newItemDrawer() itemDrawer { return nil }
