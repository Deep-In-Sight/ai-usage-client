package trayhelper

import "fyne.io/systray"

// setTrayID gives this helper's notification icon its own id (design D3):
// Windows keeps the user's show/hide setting per program and id.
func setTrayID(id uint32) { systray.SetTrayID(id) }

// iconShown reports whether the icon is in the notification area, adding
// it when the first add failed (the taskbar was not ready at login) or a
// re-add after Explorer restarted did.
func iconShown() bool { return systray.EnsureIcon() }
