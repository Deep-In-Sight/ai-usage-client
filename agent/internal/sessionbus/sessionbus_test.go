package sessionbus

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Review m4: a run that keeps the old session's environment (the restart
// in place) moves to $XDG_RUNTIME_DIR/bus when the address it has does not
// answer and a bus answers there.
func TestFallback(t *testing.T) {
	alive := map[string]bool{"unix:path=/run/user/1000/bus": true, "unix:abstract=/tmp/dbus-live,guid=1": true,
		"unix:path=run/user/1000/bus": true}
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
		{"unix:abstract=/tmp/dbus-old,guid=9", "run/user/1000", "", false},   // a relative runtime folder (as Address: review m9)
	}
	for _, c := range cases {
		got, ok := Fallback(env(c.addr, c.rt), reachable)
		if got != c.want || ok != c.ok {
			t.Errorf("%q %q: got %q %v, want %q %v", c.addr, c.rt, got, ok, c.want, c.ok)
		}
	}
}

func TestReachable(t *testing.T) {
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
	if !Reachable("unix:path=" + p) {
		t.Fatal("a listening socket is not reachable")
	}
	if !Reachable("unix:path=/nonexistent/a;unix:path=" + p + ",guid=x") {
		t.Fatal("the second address of a list answers")
	}
	if Reachable("unix:path=" + filepath.Join(dir, "none")) {
		t.Fatal("a missing socket is reachable")
	}
	if !Reachable("tcp:host=192.0.2.1,port=1") {
		t.Fatal("other transports are not judged")
	}
}

// The address a helper dials (plan PR-14, ruling F26): the given one,
// the runtime folder's when the given one is dead and that one answers, the
// runtime folder's when none is given and it answers; never anything else.
func TestAddress(t *testing.T) {
	alive := map[string]bool{"unix:path=/run/user/1000/bus": true, "unix:path=/tmp/live": true}
	reachable := func(a string) bool { return alive[a] }
	for _, c := range []struct {
		addr, rt, want string
		err            bool
	}{
		{"unix:path=/tmp/live", "/run/user/1000", "unix:path=/tmp/live", false},
		{"unix:path=/tmp/dead", "/run/user/1000", "unix:path=/run/user/1000/bus", false},
		{"unix:path=/tmp/dead", "/run/user/1001", "unix:path=/tmp/dead", false}, // dialing fails then
		{"", "/run/user/1000", "unix:path=/run/user/1000/bus", false},
		{"", "/run/user/1000/", "unix:path=/run/user/1000/bus", false},
		{"", "/run/user/1001", "", true},
		{"", "relative/run", "", true},
		{"", "", "", true},
		{"autolaunch:", "/run/user/1000", "", true},
		{"unix:path=/tmp/dead;autolaunch:", "/run/user/1001", "", true},
	} {
		env := func(k string) string {
			switch k {
			case "DBUS_SESSION_BUS_ADDRESS":
				return c.addr
			case "XDG_RUNTIME_DIR":
				return c.rt
			}
			return ""
		}
		got, err := Address(env, reachable)
		if got != c.want || (err != nil) != c.err || (err != nil && !errors.Is(err, ErrNoBus)) {
			t.Errorf("%q %q: %q %v", c.addr, c.rt, got, err)
		}
	}
}
