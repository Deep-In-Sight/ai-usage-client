//go:build !linux

package tray

import "context"

// Available reports whether a tray icon can be shown: on Windows and macOS
// whenever the tray is compiled in (Windows waits for the taskbar itself).
func Available(context.Context, func(string) string) (bool, string) {
	if !Compiled {
		return false, "tray not compiled into this build"
	}
	return true, ""
}
