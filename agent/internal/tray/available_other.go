//go:build !linux

package tray

import (
	"context"
	"time"
)

// Available reports whether a tray icon can be shown: on Windows and macOS
// whenever the tray is compiled in (Windows waits for the taskbar itself).
func Available(context.Context, func(string) string) (bool, string) {
	if !Compiled {
		return false, "tray not compiled into this build"
	}
	return true, ""
}

// WaitForHost is Linux only: elsewhere Available never reports a missing
// tray host, so there is nothing to wait for.
func WaitForHost(context.Context, time.Duration) bool { return Compiled }
