//go:build !windows

package trayhelper

import "testing"

// Elsewhere the icon does not depend on a tray icon size.
func TestTrayIconSizeIsZeroOffWindows(t *testing.T) {
	if got := trayIconSize(); got != 0 {
		t.Fatalf("trayIconSize %d", got)
	}
}
