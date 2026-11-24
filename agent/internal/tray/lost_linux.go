package tray

import "fyne.io/systray"

// sessionLost is closed when the tray's session bus connection ends.
func sessionLost() <-chan struct{} { return systray.ConnectionLost() }
