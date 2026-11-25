package tray

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sessionbus"
)

// openURL asks the desktop portal to open u, and falls back to gio. The
// portal is asked on the session bus at sessionbus.Address: never the
// D-Bus library's own lookup, which starts a bus (dbus-launch) when it
// finds none (plan RF3).
func openURL(u string) error {
	if conn, err := connectSession(); err == nil {
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
		call := obj.CallWithContext(ctx, "org.freedesktop.portal.OpenURI.OpenURI", 0, "", u, map[string]dbus.Variant{})
		if call.Err == nil {
			return nil
		}
	}
	cmd := exec.Command("gio", "open", u)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func connectSession() (*dbus.Conn, error) {
	addr, err := sessionbus.Address(os.Getenv, sessionbus.Reachable)
	if err != nil {
		return nil, err
	}
	return dbus.Connect(addr)
}
