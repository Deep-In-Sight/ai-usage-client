// Package shellbridge is the GNOME bridge (drawn-panel design 6.4): the
// tray helper in the shell role ("ai-usage tray-helper", hello role
// "shell"). It owns a name on the user's session bus and serves the usage
// panel the main process sends it to the display-only GNOME Shell
// extension, taking calls from the shell only; it tells the main process
// whether the extension is attached and passes clicks back. It writes the
// extension's files for the running shell (gnomeext.Write) and, once,
// turns the extension on. Like an icon helper it ends with 75 when the
// session bus goes away; with 78 when the session has no GNOME Shell the
// panel supports. Everything it does on the bus lives in Linux-only files:
// on other systems Run only ends with 78.
package shellbridge

import (
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// Options are the bridge's inputs.
type Options struct {
	// EnableOnce: turn the panel extension on once (from the hello; the main
	// process decides at each start, design 6.6)
	EnableOnce bool
	Getenv     func(string) string // the environment (nil: the process's)
	Home       string              // the user's home folder (the extension folder's fall-back)
	Clock      clock.Clock         // nil: the wall clock
}
