// Package paths decides where the agent keeps its data and where install
// puts the executable, per OS.
package paths

import (
	"os"
	"path/filepath"
)

// File names inside the data folder.
const (
	ConfigFile   = "config.json"
	DeviceIDFile = "device-id"
	StateFile    = "state.json"
	BacklogFile  = "backlog.json"
	LatestFile   = "latest.json"
	StatusFile   = "status.json"
	LockFile     = "run.lock"
	NotifyFile   = "notify-pending" // install writes it; the next tray run shows the notice once
	LogFile      = "agent.log"      // where a run without a console writes its log
	TrayIconsDir = "tray-icons"     // the Windows tray's icon files
)

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
