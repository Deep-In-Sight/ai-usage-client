// Package text holds every sentence the tray and the install, uninstall and
// run commands show to people, in one place. All of it is English.
package text

// Tray menu, title and tooltip (design D4, 6.5).
const (
	App             = "AI Usage"
	ReplyUnreadable = "Usage reply could not be read"
	Connecting      = "Connecting to the server…"
	Connected       = "Server connected"
	NoUsageYet      = "no usage yet"
	Unreachable     = "Server not reachable · this PC fetches every 30 min"
	ThisPCFetches   = "this PC fetches " // + provider names
	Fetching        = "fetching: "       // + the poller's host name (one section)
	FetchedFrom     = " from "           // "<provider> from <host>" (several sections)
	OnePC           = "1 PC"
	PCsSuffix       = " PCs"
	Blocked         = " blocked"
	ResetDue        = "due"
	MoreLimits      = "More limits on the dashboard"
	SignInClaude    = "Sign in to Claude Code"
	SignInAny       = "Sign in to Claude Code or Codex"
	NewVersion      = "New version available: "
	Dashboard       = "Open dashboard"
	Refresh         = "Refresh now"
	Refreshing      = "Refreshing…"
	Autostart       = "Start at login"
	Remove          = "Remove AI Usage"
	RemoveArmed     = "Click again to remove AI Usage"
	Quit            = "Quit"
	StaleMark       = " ⚠"
	Sep             = " · "
)

// A limit row's reset line in the usage panel (drawn-panel design 5.1):
// "Resets Fri 21:20 · in 2h 20m", "Reset due Fri 18:00", "Blocked · resets
// Fri 19:35 · in 35m", "Blocked · reset due Fri 18:00", "Blocked".
const (
	ResetsAt        = "Resets "    // + day and time
	ResetsIn        = " · in "     // + time left
	ResetDueAt      = "Reset due " // + day and time
	BlockedResets   = "Blocked · resets "
	BlockedResetDue = "Blocked · reset due "
	BlockedAlone    = "Blocked" // blocked, no reset time reported
)

// What a screen reader says after a drawn row's or header's texts for
// what the usage panel shows only by colour or opacity (review A3-M6).
const (
	SaysWarning    = "warning"
	SaysCritical   = "critical"
	SaysNotCurrent = "not current"
)

// Problem lines of a provider's section (design 6.5).
const (
	ClaudeExpired = "Sign-in expired: open Claude Code to renew it"
	CodexExpired  = "Codex sign-in expired: run Codex once to renew it"
	CodexKeychain = "Codex sign-in is stored in the keychain: not supported"
	CodexAPIKey   = "Codex API-key sign-in has no plan limits"
	CodexBaseURL  = "Codex server address in config.toml is not supported"
	ServerNoCodex = "Server does not support Codex yet"
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
	InstalledWindows    = "The AI Usage icons (Clawd for Claude, a ring for Codex) are under the ^ arrow on the taskbar. Drag them onto the taskbar to keep them in view."
	InstalledDarwin     = "Look for the AI Usage icons in the menu bar: Clawd for Claude, a ring for Codex, or one grey ring until Claude Code or Codex is signed in."
	InstalledLinux      = "Look for the AI Usage icons in the top bar: Clawd for Claude, a ring for Codex, or one grey ring until Claude Code or Codex is signed in."
	PanelAfterRelogin   = "The AI Usage panel appears after you log out and log in again."
	GnomeExtensionNote  = "Note: the Claude Usage GNOME extension is also installed and makes its own requests. Turn it off with: gnome-extensions disable claude-usage@localhost"
	NotLasting          = "autostart could not be registered; the agent runs until you log out or restart"
	AutostartFailed     = "Start at login could not be set up (%v). AI Usage is running now, but it will not start by itself after you log out or restart."
	LingerOff           = "linger is not enabled: the agent runs only while %s is logged in. To keep it running, ask an administrator to run: loginctl enable-linger %s"
	SignInFound         = "found"
	AutostartStaysOff   = "Start at login stays off, as set in the AI Usage menu."
	KeptClaudeDir       = "(kept from the previous install; set CLAUDE_CONFIG_DIR to change it)"
	KeptCodexHome       = "(kept from the previous install; set CODEX_HOME to change it)"
	CodexSignInExpired  = "found, but it needs renewing (run Codex once)"
	CodexSignInKeychain = "in the system keychain (not supported)"
	CodexSignInAPIKey   = "an API key (no plan limits)"
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
