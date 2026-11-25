// Package sessionbus decides which session bus address a helper dials
// (the tray helpers and the GNOME bridge share the rule, pre-flight ruling
// F26). It never starts a bus and imports no D-Bus library.
package sessionbus

import (
	"errors"
	"net"
	"path"
	"strings"
	"time"
)

const addressEnv = "DBUS_SESSION_BUS_ADDRESS"

// Fallback decides whether a process should use the bus in
// $XDG_RUNTIME_DIR instead of the address it was given: a run that keeps
// an earlier session's environment (the restart in place after a lost
// bus) can have an address that died with that session (desktops that
// give each session its own bus address), while the user's bus is in the
// runtime folder (review m4). The tray host wait asks only after the
// address failed fallbackAfter times in a row. It returns that address when the given
// one does not answer and the runtime folder's does; ok is false when
// nothing should change. An empty address is left alone: the D-Bus library
// looks in $XDG_RUNTIME_DIR by itself then.
func Fallback(getenv func(string) string, reachable func(addr string) bool) (addr string, ok bool) {
	cur, rt := getenv(addressEnv), getenv("XDG_RUNTIME_DIR")
	if cur == "" || rt == "" {
		return "", false
	}
	runtimeBus := "unix:path=" + strings.TrimRight(rt, "/") + "/bus"
	if cur == runtimeBus || strings.HasPrefix(cur, runtimeBus+",") || reachable(cur) || !reachable(runtimeBus) {
		return "", false
	}
	return runtimeBus, true
}

// Reachable reports whether a D-Bus address (one or more
// ";"-separated unix:path= or unix:abstract= entries) has a socket that
// accepts a connection. Other transports are not judged (reachable).
func Reachable(addr string) bool {
	for _, entry := range strings.Split(addr, ";") {
		kind, rest, found := strings.Cut(entry, ":")
		if !found || kind != "unix" {
			return true
		}
		for _, kv := range strings.Split(rest, ",") {
			k, v, _ := strings.Cut(kv, "=")
			var name string
			switch k {
			case "path":
				name = v
			case "abstract":
				name = "@" + v
			default:
				continue
			}
			if c, err := net.DialTimeout("unix", name, time.Second); err == nil {
				c.Close()
				return true
			}
		}
	}
	return false
}

// ErrNoBus: no session bus address to dial.
var ErrNoBus = errors.New("no session bus: no DBUS_SESSION_BUS_ADDRESS and no bus in $XDG_RUNTIME_DIR")

// Address is the session bus address to dial without ever starting a bus
// (plan PR-14, RF3): DBUS_SESSION_BUS_ADDRESS, or $XDG_RUNTIME_DIR/bus
// instead when the given address does not answer and that one does
// (Fallback); without an address, $XDG_RUNTIME_DIR/bus when its socket
// answers; else ErrNoBus. It does not judge the given address beyond
// that: a dead address without a live runtime bus is returned as it is
// (dialing it fails). An autolaunch: address is refused: dialing it would
// start a bus.
func Address(getenv func(string) string, reachable func(addr string) bool) (string, error) {
	if cur := getenv(addressEnv); strings.Contains(cur, "autolaunch:") {
		// the D-Bus library would start a bus for it (dbus-launch)
		return "", ErrNoBus
	} else if cur != "" {
		if alt, ok := Fallback(getenv, reachable); ok {
			return alt, nil
		}
		return cur, nil
	}
	rt := getenv("XDG_RUNTIME_DIR")
	if !path.IsAbs(rt) { // a POSIX path whatever the OS
		return "", ErrNoBus
	}
	runtimeBus := "unix:path=" + strings.TrimRight(rt, "/") + "/bus"
	if !reachable(runtimeBus) {
		return "", ErrNoBus
	}
	return runtimeBus, nil
}
