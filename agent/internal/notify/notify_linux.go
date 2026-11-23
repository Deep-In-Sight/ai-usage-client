package notify

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"
)

// Show sends a notification through org.freedesktop.Notifications.
func Show(title, body string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.CallWithContext(ctx, "org.freedesktop.Notifications.Notify", 0,
		"Claude Usage", uint32(0), "", title, body, []string{}, map[string]dbus.Variant{}, int32(10000)).Err
}
