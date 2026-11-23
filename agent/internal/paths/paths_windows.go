package paths

import (
	"errors"
	"path/filepath"
)

// Windows uses the non-roaming LOCALAPPDATA so a roaming profile does not
// carry the device id to another PC.
func forOS(getenv func(string) string, home string) (Dirs, error) {
	local := getenv("LOCALAPPDATA")
	if local == "" {
		return Dirs{}, errors.New("paths: LOCALAPPDATA is not set")
	}
	return Dirs{
		Data:       filepath.Join(local, "ClaudeUsage"),
		InstallExe: filepath.Join(local, "Programs", "ClaudeUsage", "claude-usage.exe"),
		Home:       home,
	}, nil
}
