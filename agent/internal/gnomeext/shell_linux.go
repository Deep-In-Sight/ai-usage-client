//go:build linux

package gnomeext

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sessionbus"
)

// DialSession connects to the session bus at the address the helpers use
// (sessionbus.Address: DBUS_SESSION_BUS_ADDRESS, else or instead the bus
// in $XDG_RUNTIME_DIR; plan PR-14, ruling F26). It never starts a bus, as
// the D-Bus library's own session-bus lookup would (dbus-launch).
func DialSession(getenv func(string) string) (*dbus.Conn, error) {
	addr, err := sessionbus.Address(getenv, sessionbus.Reachable)
	if err != nil {
		return nil, err
	}
	return dbus.Connect(addr)
}

// ShellMajor reads GNOME Shell's ShellVersion ("46.0") and its major
// version (46).
func ShellMajor(ctx context.Context, c *dbus.Conn) (major int, version string, err error) {
	var v dbus.Variant
	err = c.Object("org.gnome.Shell", "/org/gnome/Shell").CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get",
		dbus.FlagNoAutoStart, "org.gnome.Shell", "ShellVersion").Store(&v)
	if err != nil {
		return 0, "", err
	}
	version, ok := v.Value().(string)
	if !ok {
		return 0, "", fmt.Errorf("ShellVersion is a %s, not a string", v.Signature())
	}
	major, err = parseMajor(version)
	return major, version, err
}

// parseMajor is the major version of a shell version: "46.0" and "50.1"
// and "42.beta" give 46, 50 and 42.
func parseMajor(version string) (int, error) {
	head, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	n, err := strconv.Atoi(head)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a GNOME Shell version: %q", version)
	}
	return n, nil
}

// Enable asks the shell to turn the extension uuid on
// (org.gnome.Shell.Extensions.EnableExtension, as stage 0 recorded it:
// false for an extension the shell did not find when it started).
func Enable(ctx context.Context, c *dbus.Conn, uuid string) (bool, error) {
	return extensionsCall(ctx, c, "EnableExtension", uuid)
}

// Disable asks the shell to turn the extension uuid off (the shell also
// lists it in disabled-extensions).
func Disable(ctx context.Context, c *dbus.Conn, uuid string) (bool, error) {
	return extensionsCall(ctx, c, "DisableExtension", uuid)
}

func extensionsCall(ctx context.Context, c *dbus.Conn, method, uuid string) (bool, error) {
	var ok bool
	err := c.Object("org.gnome.Shell.Extensions", "/org/gnome/Shell/Extensions").CallWithContext(ctx,
		"org.gnome.Shell.Extensions."+method, dbus.FlagNoAutoStart, uuid).Store(&ok)
	return ok, err
}
