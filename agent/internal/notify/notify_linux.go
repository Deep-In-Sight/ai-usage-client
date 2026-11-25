package notify

import (
	"context"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sessionbus"
)

// Show sends a notification through org.freedesktop.Notifications on the
// session bus at sessionbus.Address: never the D-Bus library's own
// lookup, which starts a bus (dbus-launch) when it finds none (plan RF3).
func Show(title, body string) error {
	addr, err := sessionbus.Address(os.Getenv, sessionbus.Reachable)
	if err != nil {
		return err
	}
	conn, err := dbus.Connect(addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.CallWithContext(ctx, "org.freedesktop.Notifications.Notify", 0,
		"AI Usage", uint32(0), "", title, body, []string{}, map[string]dbus.Variant{}, int32(10000)).Err
}
