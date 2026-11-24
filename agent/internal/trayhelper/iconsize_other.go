//go:build !windows

package trayhelper

// trayIconSize: only Windows picks the icon entry by size.
func trayIconSize() int { return 0 }
