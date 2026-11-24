package tray

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// Available reports whether a tray icon can be shown, and why not. On Linux
// it needs a desktop session, a session bus and a StatusNotifierWatcher
// (Ubuntu's AppIndicator extension, KDE), which it waits for up to
// WatcherWaitFrom(getenv) after login.
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
		err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, watcherName).Store(&has)
		if err != nil {
			return false, errNoBus
		}
		return has, nil
	}
	return availableWith(ctx, getenv, hasOwner, clock.Real{})
}

// WaitForHost waits until the desktop's tray host (a
// StatusNotifierWatcher) is on the session bus, noticing its name the
// moment it appears; it reconnects when the bus goes away. It returns
// false when ctx ends or after limit (0: no limit).
func WaitForHost(ctx context.Context, limit time.Duration) bool {
	return waitForHost(ctx, dialHost, clock.Real{}, limit)
}

// dbusHost is a private session bus connection that watches the
// watcher's name.
type dbusHost struct {
	conn     *dbus.Conn
	appeared chan struct{}
}

func dialHost() (hostConn, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		sessionBus.failed()
		return nil, err
	}
	sessionBus.ok()
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	); err != nil {
		conn.Close()
		return nil, err
	}
	sigs := make(chan *dbus.Signal, 8)
	conn.Signal(sigs)
	h := &dbusHost{conn: conn, appeared: make(chan struct{}, 1)}
	go func() {
		for s := range sigs { // closed when the connection closes
			// NameOwnerChanged(name, old owner, new owner)
			if len(s.Body) == 3 {
				if owner, _ := s.Body[2].(string); owner != "" {
					select {
					case h.appeared <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return h, nil
}

func (h *dbusHost) Has(ctx context.Context) (bool, error) {
	var has bool
	err := h.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, watcherName).Store(&has)
	return has, err
}
func (h *dbusHost) Appeared() <-chan struct{} { return h.appeared }
func (h *dbusHost) Closed() <-chan struct{}   { return h.conn.Context().Done() }
func (h *dbusHost) Close()                    { h.conn.Close() }
