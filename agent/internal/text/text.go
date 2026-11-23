// Package text holds every sentence the tray and the install, uninstall and
// run commands show to people, in one place. All of it is English.
package text

// Tray menu, title and tooltip.
const (
	App           = "Claude Usage"
	SignInNeeded  = "Claude Code sign-in needed"
	AllModels     = "All models"
	Connecting    = "Connecting to the server…"
	Connected     = "Server connected"
	NoUsageYet    = "no usage yet"
	Unreachable   = "Server not reachable  ·  this PC fetches every 30 min"
	ThisPCFetches = "this PC fetches"
	Poller        = "poller "
	Account       = "Account "
	OnePC         = "1 PC on this account"
	PCsSuffix     = " PCs on this account"
	NewVersion    = "New version available: "
	Dashboard     = "Open dashboard"
	Refresh       = "Refresh now"
	Refreshing    = "Refreshing…"
	Autostart     = "Start at login"
	Remove        = "Remove Claude Usage"
	RemoveArmed   = "Click again to remove Claude Usage"
	Quit          = "Quit"
	StaleMark     = " ⚠"
	Sep           = "  ·  "
)

// Notices shown once after an install or upgrade (Linux and Windows as an
// OS notification, macOS as the menu bar title for a while).
const (
	NoticeWindows = "Claude Usage is running. Its icon is under the ^ arrow on the taskbar; drag it onto the taskbar to keep it in view."
	NoticeLinux   = "Claude Usage is running. Its icon is in the top bar."
	NoticeDarwin  = "Claude Usage is running"
	RemoveFailed  = "Claude Usage could not be removed completely: "
)

// Notice is the once-only notice for goos.
func Notice(goos string) string {
	switch goos {
	case "windows":
		return NoticeWindows
	case "darwin":
		return NoticeDarwin
	}
	return NoticeLinux
}
