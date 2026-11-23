package autostart

import "context"

// Toggler is the tray's "Start at login" switch: it only writes or removes
// the autostart entry and never starts or stops a process.
type Toggler interface {
	Enabled(ctx context.Context) bool
	SetEnabled(ctx context.Context, exe string, on bool) error
}
