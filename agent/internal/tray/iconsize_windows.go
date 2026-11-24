package tray

import "fyne.io/systray/trayicon"

// trayIconSize is the size the notification area draws icons at now (the
// patched systray loads the ICO entry of this size).
func trayIconSize() int {
	cx, _ := trayicon.Size()
	return int(cx)
}
