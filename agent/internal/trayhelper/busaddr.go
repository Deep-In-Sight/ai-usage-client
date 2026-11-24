package trayhelper

import (
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const busEnv = "DBUS_SESSION_BUS_ADDRESS"

// SessionBusFallback decides whether this process should use the bus in
// $XDG_RUNTIME_DIR instead of the address it was given: a run that keeps
// an earlier session's environment (the restart in place after a lost
// bus) can have an address that died with that session (desktops that
// give each session its own bus address), while the user's bus is in the
// runtime folder (review m4). The tray host wait asks only after the
// address failed fallbackAfter times in a row. It returns that address when the given
// one does not answer and the runtime folder's does; ok is false when
// nothing should change. An empty address is left alone: the D-Bus library
// looks in $XDG_RUNTIME_DIR by itself then.
func SessionBusFallback(getenv func(string) string, reachable func(addr string) bool) (addr string, ok bool) {
	cur, rt := getenv(busEnv), getenv("XDG_RUNTIME_DIR")
	if cur == "" || rt == "" {
		return "", false
	}
	runtimeBus := "unix:path=" + strings.TrimRight(rt, "/") + "/bus"
	if cur == runtimeBus || strings.HasPrefix(cur, runtimeBus+",") || reachable(cur) || !reachable(runtimeBus) {
		return "", false
	}
	return runtimeBus, true
}

// BusReachable reports whether a D-Bus address (one or more
// ";"-separated unix:path= or unix:abstract= entries) has a socket that
// accepts a connection. Other transports are not judged (reachable).
func BusReachable(addr string) bool {
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

// fallbackAfter is how many failed connects in a row (one every hostRetry,
// so about two minutes) move a process to the runtime folder's bus. A bus
// that restarts at its address is back long before.
const fallbackAfter = 60

// busSwitch counts failed session bus connects and, after fallbackAfter
// in a row, points this process (and so the tray library too) at the
// runtime folder's bus when one answers there.
type busSwitch struct {
	mu        sync.Mutex
	failures  int
	getenv    func(string) string
	setenv    func(k, v string)
	reachable func(string) bool
}

var sessionBus = &busSwitch{
	getenv:    os.Getenv,
	setenv:    func(k, v string) { _ = os.Setenv(k, v) },
	reachable: BusReachable,
}

func (b *busSwitch) ok() {
	b.mu.Lock()
	b.failures = 0
	b.mu.Unlock()
}

func (b *busSwitch) failed() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures < fallbackAfter {
		return
	}
	b.failures = 0
	if addr, ok := SessionBusFallback(b.getenv, b.reachable); ok {
		log.Printf("session bus: the address this run was given does not answer; using %s", addr)
		b.setenv(busEnv, addr)
	}
}
