package trayhelper

import (
	"testing"
	"time"
)

// Only after the address failed for about two minutes in a row
// (fallbackAfter connects, one every hostRetry) does the process move to
// the runtime bus; a bus that restarts at its address comes back long
// before (CI tray.bus_restart), and a success resets the count.
func TestBusSwitchMovesOnlyAfterTheAddressStaysDead(t *testing.T) {
	if time.Duration(fallbackAfter)*hostRetry < 2*time.Minute {
		t.Fatalf("fallback after %v of failures", time.Duration(fallbackAfter)*hostRetry)
	}
	vars := map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:abstract=/tmp/dbus-old", "XDG_RUNTIME_DIR": "/run/user/1000"}
	b := &busSwitch{
		getenv:    func(k string) string { return vars[k] },
		setenv:    func(k, v string) { vars[k] = v },
		reachable: func(a string) bool { return a == "unix:path=/run/user/1000/bus" },
	}
	for i := 0; i < fallbackAfter-1; i++ {
		b.failed()
	}
	b.ok()
	for i := 0; i < fallbackAfter-1; i++ {
		b.failed()
	}
	if vars["DBUS_SESSION_BUS_ADDRESS"] != "unix:abstract=/tmp/dbus-old" {
		t.Fatal("moved before the address stayed dead long enough")
	}
	b.failed()
	if got := vars["DBUS_SESSION_BUS_ADDRESS"]; got != "unix:path=/run/user/1000/bus" {
		t.Fatalf("address %q", got)
	}
}
