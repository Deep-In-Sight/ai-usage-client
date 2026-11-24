package tray

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Review m4: a run that keeps the old session's environment (the restart
// in place) moves to $XDG_RUNTIME_DIR/bus when the address it has does not
// answer and a bus answers there.
func TestSessionBusFallback(t *testing.T) {
	alive := map[string]bool{"unix:path=/run/user/1000/bus": true, "unix:abstract=/tmp/dbus-live,guid=1": true}
	reachable := func(a string) bool { return alive[a] }
	env := func(addr, rt string) func(string) string {
		return func(k string) string {
			switch k {
			case "DBUS_SESSION_BUS_ADDRESS":
				return addr
			case "XDG_RUNTIME_DIR":
				return rt
			}
			return ""
		}
	}
	cases := []struct {
		addr, rt, want string
		ok             bool
	}{
		{"unix:abstract=/tmp/dbus-old,guid=9", "/run/user/1000", "unix:path=/run/user/1000/bus", true},
		{"unix:abstract=/tmp/dbus-live,guid=1", "/run/user/1000", "", false}, // the address answers
		{"unix:path=/run/user/1000/bus", "/run/user/1000", "", false},        // already there
		{"unix:abstract=/tmp/dbus-old,guid=9", "", "", false},                // no runtime folder
		{"unix:abstract=/tmp/dbus-old,guid=9", "/run/user/1001", "", false},  // nothing answers there either
		{"", "/run/user/1000", "", false},                                    // godbus falls back by itself
	}
	for _, c := range cases {
		got, ok := SessionBusFallback(env(c.addr, c.rt), reachable)
		if got != c.want || ok != c.ok {
			t.Errorf("%q %q: got %q %v, want %q %v", c.addr, c.rt, got, ok, c.want, c.ok)
		}
	}
}

func TestBusReachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets: Linux and macOS")
	}
	dir, err := os.MkdirTemp("", "bus")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "bus")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !BusReachable("unix:path=" + p) {
		t.Fatal("a listening socket is not reachable")
	}
	if !BusReachable("unix:path=/nonexistent/a;unix:path=" + p + ",guid=x") {
		t.Fatal("the second address of a list answers")
	}
	if BusReachable("unix:path=" + filepath.Join(dir, "none")) {
		t.Fatal("a missing socket is reachable")
	}
	if !BusReachable("tcp:host=192.0.2.1,port=1") {
		t.Fatal("other transports are not judged")
	}
}

// Only after the address failed for about two minutes in a row
// (fallbackAfter connects, one every hostRetry) does the process move to
// the runtime bus; a bus that restarts at its address comes back long
// before (CI tray.bus_restart), and a success resets the count.
func TestBusSwitchMovesOnlyAfterTheAddressStaysDead(t *testing.T) {
	if time.Duration(fallbackAfter)*hostRetry < 2*time.Minute {
		t.Fatalf("fallback after %v of failures", time.Duration(fallbackAfter)*hostRetry)
	}
	vars := map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:abstract=/tmp/dbus-old", "XDG_RUNTIME_DIR": "/run/user/1000"}
	b := &busSwitch{
		getenv:    func(k string) string { return vars[k] },
		setenv:    func(k, v string) { vars[k] = v },
		reachable: func(a string) bool { return a == "unix:path=/run/user/1000/bus" },
	}
	for i := 0; i < fallbackAfter-1; i++ {
		b.failed()
	}
	b.ok()
	for i := 0; i < fallbackAfter-1; i++ {
		b.failed()
	}
	if vars["DBUS_SESSION_BUS_ADDRESS"] != "unix:abstract=/tmp/dbus-old" {
		t.Fatal("moved before the address stayed dead long enough")
	}
	b.failed()
	if got := vars["DBUS_SESSION_BUS_ADDRESS"]; got != "unix:path=/run/user/1000/bus" {
		t.Fatalf("address %q", got)
	}
}
