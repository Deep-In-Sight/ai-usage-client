package trayhelper

import (
	"log"
	"os"
	"sync"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sessionbus"
)

const busEnv = "DBUS_SESSION_BUS_ADDRESS"

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
	reachable: sessionbus.Reachable,
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
	if addr, ok := sessionbus.Fallback(b.getenv, b.reachable); ok {
		log.Printf("session bus: the address this run was given does not answer; using %s", addr)
		b.setenv(busEnv, addr)
	}
}
