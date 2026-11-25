package tray

import (
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
)

// RF3: without a session bus a link is opened by gio alone and no bus is
// started (dbus-launch), also for an "autolaunch:" address. (gio here is
// a recorder: nothing is opened.)
func TestOpenURLNeverAutolaunches(t *testing.T) {
	_, ran := fakeshell.NoBus(t)
	for _, addr := range []string{"", "autolaunch:"} {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
		_ = OpenURL("https://example.com/usage")
	}
	if r := ran("dbus-launch"); r != "" {
		t.Fatalf("dbus-launch ran: %s", r)
	}
	// gio runs in the background
	for end := time.Now().Add(5 * time.Second); strings.Count(ran("gio"), "open https://example.com/usage") != 2; {
		if time.Now().After(end) {
			t.Fatalf("gio ran: %q", ran("gio"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
