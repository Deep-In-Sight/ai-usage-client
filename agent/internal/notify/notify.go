// Package notify shows a one-off OS notification: the freedesktop
// notification service on Linux and a notification-area balloon on Windows.
// macOS has none without an app bundle (the tray shows its title instead).
package notify

import "errors"

// ErrUnsupported: no notifications on this OS.
var ErrUnsupported = errors.New("notify: not supported on this OS")
