package autostart

import (
	"context"
	"os"
)

// Toggler is the tray's "Start at login" switch: it only writes or removes
// the autostart entry and never starts or stops a process.
type Toggler interface {
	Enabled(ctx context.Context) bool
	SetEnabled(ctx context.Context, exe string, on bool) error
}

// Preflighter checks, before anything is stopped or replaced, that the
// autostart entry can be written (the folder or registry key is writable).
type Preflighter interface {
	Preflight(ctx context.Context) error
}

// probeDir creates dir if needed and writes and removes a small file in it.
func probeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ai-usage-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
