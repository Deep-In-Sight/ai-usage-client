package tray

import (
	"context"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/clock"
)

// Available reports whether a tray icon can be shown, and why not. On Linux
// it needs a desktop session, a session bus and a StatusNotifierWatcher
// (Ubuntu's AppIndicator extension, KDE), which may appear up to
// WatcherWait after login.
func Available(ctx context.Context, getenv func(string) string) (bool, string) {
	var conn *dbus.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	hasOwner := func() (bool, error) {
		if conn == nil {
			c, err := dbus.ConnectSessionBus()
			if err != nil {
				return false, errNoBus
			}
			conn = c
		}
		var has bool
		err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
		if err != nil {
			return false, errNoBus
		}
		return has, nil
	}
	return availableWith(ctx, getenv, hasOwner, clock.Real{})
}
