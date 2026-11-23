package autostart

import (
	"os"
	"path/filepath"
)

func forOS(o Options) (Method, error) {
	return &LaunchAgent{Home: o.Home, LogPath: filepath.Join(o.DataDir, "agent.log"), UID: os.Getuid(), Exe: o.Exe, Run: o.Run}, nil
}

func othersOS(Options) []Method { return nil }
