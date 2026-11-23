package tray

import (
	"context"
	"os/exec"
	"time"

	"github.com/godbus/dbus/v5"
)

// openURL asks the desktop portal to open u, and falls back to gio.
func openURL(u string) error {
	if conn, err := dbus.ConnectSessionBus(); err == nil {
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
