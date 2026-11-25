// Package paths decides where the agent keeps its data and where install
// puts the executable, per OS.
package paths

import (
	"os"
	"path/filepath"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// File names inside the data folder.
const (
	ConfigFile       = "config.json"
	DeviceIDFile     = "device-id"
	StateFile        = "state.json"
	BacklogFile      = "backlog.json"
	LatestFile       = "latest.json" // v0.7/v0.8: Claude's; renamed to latest-claude.json at the first start
	StatusFile       = "status.json"
	LockFile         = "run.lock"
	NotifyFile       = "notify-pending" // install writes it; the next tray run shows the notice once
	LogFile          = "agent.log"      // where a run without a console writes its log
	TrayIconsDir     = "tray-icons"     // the Windows tray's icon files
	AutostartOffFile = "autostart-off"  // "Start at login" was turned off in the tray
	// The GNOME usage panel (drawn-panel design 6.4, 6.6; plan PR-12),
	// written and removed by the main process only: the panel was attached
	// in this or an earlier session (the icons wait for it at the next
	// start), and the bridge turned the extension on once (never again).
	PanelAttachedFile = "gnome-panel-attached"
	PanelEnabledFile  = "gnome-panel-enabled"
)

// LatestFileFor is the file with the newest numbers of one provider:
// latest-claude.json, latest-codex.json.
func LatestFileFor(id provider.ID) string { return "latest-" + string(id) + ".json" }

// Dirs are the agent's locations for the current OS user.
type Dirs struct {
	Data       string // data folder (config, device id, backlog, status)
	InstallExe string // where install copies the executable
	Home       string // the user's home folder
}

// File returns the path of name inside the data folder.
func (d Dirs) File(name string) string { return filepath.Join(d.Data, name) }

// For applies the current OS's rules to the given environment and home.
func For(getenv func(string) string, home string) (Dirs, error) { return forOS(getenv, home) }

// Current applies the rules to this process's environment.
func Current() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	return For(os.Getenv, home)
}
