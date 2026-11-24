package cli

import (
	"slices"
	"testing"
)

// The restarted run gets the restart count and keeps the session bus
// address it had, even when nothing answers there right now: a bus that
// restarts comes back at the same address (CI tray.bus_restart). Moving to
// the runtime folder's bus is left to the wait for a tray host, after the
// address stayed dead for a while (review m4).
func TestRestartEnviron(t *testing.T) {
	in := []string{"HOME=/h", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/fixed/bus", "XDG_RUNTIME_DIR=/run/user/1000", restartsEnv + "=1"}
	got := restartEnviron(in, 2)
	want := []string{"HOME=/h", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/fixed/bus", "XDG_RUNTIME_DIR=/run/user/1000", restartsEnv + "=2"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}
