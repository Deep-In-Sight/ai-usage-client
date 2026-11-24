package paths

import (
	"errors"
	"path/filepath"
)

// Linux ignores XDG_CONFIG_HOME on purpose: the shell that ran install and
// the systemd user service may have different environments, and both must
// find the same folder.
func forOS(_ func(string) string, home string) (Dirs, error) {
	if home == "" {
		return Dirs{}, errors.New("paths: no home folder")
	}
	return Dirs{
		Data:       filepath.Join(home, ".config", "ai-usage"),
		InstallExe: filepath.Join(home, ".local", "bin", "ai-usage"),
		Home:       home,
	}, nil
}
