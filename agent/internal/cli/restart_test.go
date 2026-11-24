package cli

import (
	"slices"
	"testing"
)

// The restarted run gets the runtime folder's bus when the old session's
// address is dead (review m4), and the restart count.
func TestRestartEnviron(t *testing.T) {
	in := []string{"HOME=/h", "DBUS_SESSION_BUS_ADDRESS=unix:abstract=/tmp/dbus-old", "XDG_RUNTIME_DIR=/run/user/1000", restartsEnv + "=1"}
	alive := func(a string) bool { return a == "unix:path=/run/user/1000/bus" }
	got := restartEnviron(in, 2, alive)
	want := []string{"HOME=/h", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "XDG_RUNTIME_DIR=/run/user/1000", restartsEnv + "=2"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	got = restartEnviron(in, 2, func(string) bool { return true }) // the old address answers
	if !slices.Contains(got, "DBUS_SESSION_BUS_ADDRESS=unix:abstract=/tmp/dbus-old") {
		t.Fatalf("%q", got)
	}
}
