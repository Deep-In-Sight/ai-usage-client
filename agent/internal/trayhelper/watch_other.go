//go:build !linux && !windows

package trayhelper

import "context"

// watchSession: macOS gives a helper nothing to watch for (its menu bar is
// always there), so a stopped helper waits for its timer.
var watchSession func(ctx context.Context) bool
