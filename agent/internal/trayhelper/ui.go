package trayhelper

// ui is the tray library as a helper uses it (the real one is fyne's
// systray copy in third_party/systray; tests use a fake).
type ui interface {
	// Run starts the tray and blocks until Quit; onReady runs once it is
	// up (on its own goroutine), onExit when it ends (also when the OS
	// ends it: Windows' WM_ENDSESSION).
	Run(onReady, onExit func())
	Quit()
	AddItem(id string, checkbox bool) uiItem
	AddSeparator()
	// Reset removes every item (their Clicked channels close).
	Reset()
	SetIcon(img []byte)
	// SetNextToIcon sets the text next to the icon: the label in Ubuntu's
	// top bar, the macOS menu bar title (nothing on Windows).
	SetNextToIcon(s string)
	SetTooltip(s string)
	// Lost is closed when the session's connection went away (Linux: the
	// session bus); nil elsewhere.
	Lost() <-chan struct{}
	// Shown reports whether the icon is in the notification area; on
	// Windows it adds the icon when it is not (the taskbar was not ready).
	Shown() bool
	// IconSize is the notification area's icon size (Windows; 0 elsewhere).
	IconSize() int
}

type uiItem interface {
	SetTitle(string)
	SetEnabled(bool)
	SetVisible(bool)
	SetChecked(bool)
	SetImage([]byte)
	Clicked() <-chan struct{}
}
