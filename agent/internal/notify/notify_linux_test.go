package notify

import (
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
)

// RF3: without a session bus a notification fails and starts no bus
// (dbus-launch), also for an "autolaunch:" address.
func TestShowNeverAutolaunches(t *testing.T) {
	_, ran := fakeshell.NoBus(t)
	for _, addr := range []string{"", "autolaunch:"} {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
		if err := Show("AI Usage", "a test"); err == nil {
			t.Fatalf("%q: shown without a bus", addr)
		}
	}
	if r := ran("dbus-launch"); r != "" {
		t.Fatalf("dbus-launch ran: %s", r)
	}
}

// The notification reaches org.freedesktop.Notifications on the bus in the
// runtime folder when no address is given (a private bus).
func TestShowOnTheRuntimeFolderBus(t *testing.T) {
	rt, _ := fakeshell.NoBus(t)
	bus := fakeshell.NewBus(t, fakeshell.SocketIn(rt))
	sh := fakeshell.Start(t, bus.Addr, fakeshell.Options{Names: []string{fakeshell.NotificationsName}})
	if err := Show("AI Usage", "a test"); err != nil {
		t.Fatal(err)
	}
	if n := sh.Notices(); len(n) != 1 || n[0] != "AI Usage|AI Usage|a test" {
		t.Fatalf("%q", n)
	}
}
