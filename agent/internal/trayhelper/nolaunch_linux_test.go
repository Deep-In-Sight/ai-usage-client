package trayhelper

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
)

// RF3: a tray helper started without a session bus (a desktop's
// variables, but no bus address and no bus in the runtime folder) finds
// none and never starts one: neither its tray host checks nor the wait for
// the host run dbus-launch, also for an "autolaunch:" address.
func TestTrayHelperNeverAutolaunches(t *testing.T) {
	rt, ran := fakeshell.NoBus(t)
	for _, addr := range []string{"", "autolaunch:"} {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
		env := map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_RUNTIME_DIR": rt, "DBUS_SESSION_BUS_ADDRESS": addr}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ok, why := Available(ctx, func(k string) string { return env[k] })
		cancel()
		if ok || why != "no session bus" {
			t.Fatalf("%q: Available %v %q", addr, ok, why)
		}
		if h, err := dialHost(); err == nil {
			h.Close()
			t.Fatalf("%q: dialHost found a bus", addr)
		}
	}
	if r := ran("dbus-launch"); r != "" {
		t.Fatalf("dbus-launch ran: %s", r)
	}
}

// The tray library looks for the session bus by itself: a helper that
// found the bus in the runtime folder (no address given) gives the
// library that address, so the library never looks further or starts a
// bus.
func TestDialHostGivesTheTrayLibraryItsBus(t *testing.T) {
	rt, _ := fakeshell.NoBus(t)
	fakeshell.NewBus(t, fakeshell.SocketIn(rt))
	h, err := dialHost()
	must(t, err)
	defer h.Close()
	eq(t, os.Getenv("DBUS_SESSION_BUS_ADDRESS"), fakeshell.SocketIn(rt))
}
