package trayhelper

import "github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"

// ui is the tray library as a helper uses it (the real one is fyne's
// systray copy in third_party/systray; tests use a fake).
type ui interface {
	// Run starts the tray and blocks until Quit; onReady runs once it is
	// up (on its own goroutine), onExit when it ends (also when the OS
	// ends it: Windows' WM_ENDSESSION).
	Run(onReady, onExit func())
	Quit()
	AddItem(id string, checkbox bool) uiItem
	// AddSeparator adds a separator; drawn: one the ui draws itself (in a
	// menu whose items it draws).
	AddSeparator(drawn bool)
	// Draws reports whether this ui draws the usage panel's items itself
	// (Windows; macOS once its views come, plan Task 9). Otherwise the
	// menu is text items (pre-flight F8: asked, not decided by the OS).
	Draws() bool
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
	// SetDrawn has the ui draw the item from it (nil: a text item again);
	// nothing on a ui that does not draw.
	SetDrawn(it *panel.Item)
}

// itemDrawer keeps what each drawn item draws, by its menu id (Windows'
// drawer; nil where the ui does not draw).
type itemDrawer interface {
	Set(id uint32, it *panel.Item)
	// Clear forgets every item (the menu is rebuilt).
	Clear()
	// Failed reports that the drawing stopped for good (a panic): the
	// ui does not draw from then on.
	Failed() bool
}
