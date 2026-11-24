package tray

import "errors"

// ErrSessionLost: Run ended because the tray's connection to the desktop
// session went away (on Linux, the session bus). The tray cannot come back
// in this process.
var ErrSessionLost = errors.New("tray: the connection to the session bus was lost")
