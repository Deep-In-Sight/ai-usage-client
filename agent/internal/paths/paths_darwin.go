package paths

import (
	"errors"
	"path/filepath"
)

func forOS(_ func(string) string, home string) (Dirs, error) {
	if home == "" {
		return Dirs{}, errors.New("paths: no home folder")
	}
	data := filepath.Join(home, "Library", "Application Support", "AIUsage")
	return Dirs{Data: data, InstallExe: filepath.Join(data, "ai-usage"), Home: home}, nil
}
