// Package text holds every sentence the tray and the install, uninstall and
// run commands show to people, in one place. All of it is English.
package text

// Tray menu, title and tooltip.
const (
	App           = "AI Usage"
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
	Remove        = "Remove AI Usage"
	RemoveArmed   = "Click again to remove AI Usage"
	Quit          = "Quit"
	StaleMark     = " ⚠"
	Sep           = "  ·  "
)

// Notices shown once after an install or upgrade (Linux and Windows as an
// OS notification, macOS as the menu bar title for a while).
const (
	NoticeWindows = "AI Usage is running. Its icon is under the ^ arrow on the taskbar; drag it onto the taskbar to keep it in view."
	NoticeLinux   = "AI Usage is running. Its icon is in the top bar."
	NoticeDarwin  = "AI Usage is running"
	RemoveFailed  = "AI Usage could not be removed completely: "
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

// install and uninstall.
const (
	InstalledWindows    = "The AI Usage icon is under the ^ arrow on the taskbar. Drag it onto the taskbar to keep it in view."
	InstalledDarwin     = "Look for the AI Usage ring in the menu bar."
	InstalledLinux      = "Look for the AI Usage ring in the top bar."
	GnomeExtensionNote  = "Note: the Claude Usage GNOME extension is also installed and makes its own requests. Turn it off with: gnome-extensions disable claude-usage@localhost"
	NotLasting          = "autostart could not be registered; the agent runs until you log out or restart"
	AutostartFailed     = "Start at login could not be set up (%v). AI Usage is running now, but it will not start by itself after you log out or restart."
	LingerOff           = "linger is not enabled: the agent runs only while %s is logged in. To keep it running, ask an administrator to run: loginctl enable-linger %s"
	SignInFound         = "found"
	AutostartStaysOff   = "Start at login stays off, as set in the AI Usage menu."
	KeptClaudeDir       = "(kept from the previous install; set CLAUDE_CONFIG_DIR to change it)"
	SignInNotFound      = "not found yet (the agent starts collecting once you sign in)"
	AlertInstalled      = "AI Usage is installed and running.\n\nIts icon is under the ^ arrow on the taskbar."
	AlertInstallFailed  = "AI Usage could not be installed:\n"
	AlertRemoved        = "AI Usage has been removed."
	CannotRemoveRunning = "Could not remove %s while it is running; delete it after this window closes. It stays listed in Installed apps until then."
	Removed             = "removed"
)

// Installed is the line that says where to find the agent after install
// ("" on a Linux without a desktop).
func Installed(goos string, headless bool) string {
	switch {
	case goos == "windows":
		return InstalledWindows
	case goos == "darwin":
		return InstalledDarwin
	case headless:
		return ""
	}
	return InstalledLinux
}
