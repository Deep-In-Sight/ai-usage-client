package trayhelper

import (
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows the size the tray compares is the notification area's icon
// size, which on the (100 %) runner is the small-icon metric.
func TestTrayIconSizeIsTheSmallIconSize(t *testing.T) {
	r, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("GetSystemMetrics").Call(49) // SM_CXSMICON
	if got := trayIconSize(); got != int(r) || got < 16 {
		t.Fatalf("trayIconSize %d, SM_CXSMICON %d", got, r)
	}
}
