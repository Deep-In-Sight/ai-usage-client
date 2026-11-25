//go:build linux

package gnomeext

import (
	"context"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
)

// busID is the id of the bus c is connected to.
func busID(t *testing.T, c *dbus.Conn) string {
	t.Helper()
	var id string
	if err := c.BusObject().Call("org.freedesktop.DBus.GetId", 0).Store(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// DialSession (plan PR-14, ruling F26): the address it was given, the
// runtime folder's bus instead when that address is dead, the runtime
// folder's bus without an address, and otherwise an error: never a bus of
// its own. A DialSession broken to use the D-Bus library's own lookup
// would find this PC's user bus at /run/user/<uid>/bus when given no
// address: those cases run on CI runners, in a container or a sandbox only
// (fakeshell.UserBusHere), as TestBridgeNeverAutolaunches does.
func TestDialSessionOrder(t *testing.T) {
	live := !fakeshell.UserBusHere()
	given := fakeshell.NewBus(t, "")
	rt := fakeshell.ShortDir(t)
	inRuntime := fakeshell.NewBus(t, fakeshell.SocketIn(rt))
	empty := fakeshell.ShortDir(t)
	dead := fakeshell.SocketIn(fakeshell.ShortDir(t))
	idOf := func(addr string) string {
		c, err := dbus.Connect(addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return busID(t, c)
	}
	for _, c := range []struct {
		addr, rt, want string // want: the bus's address, "" an error
	}{
		{given.Addr, rt, given.Addr},
		{dead, rt, inRuntime.Addr},
		{"", rt, inRuntime.Addr},
		{"", empty, ""},
		{dead, empty, ""},
		{"", "", ""},
	} {
		if c.addr == "" && !live {
			t.Logf("no address, runtime folder %q: left to CI runners (this PC has a user session bus)", c.rt)
			continue
		}
		env := func(k string) string {
			return map[string]string{"DBUS_SESSION_BUS_ADDRESS": c.addr, "XDG_RUNTIME_DIR": c.rt}[k]
		}
		conn, err := DialSession(env)
		if c.want == "" {
			if err == nil {
				conn.Close()
				t.Fatalf("%q %q: connected", c.addr, c.rt)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q %q: %v", c.addr, c.rt, err)
		}
		if got := busID(t, conn); got != idOf(c.want) {
			t.Fatalf("%q %q: connected to another bus", c.addr, c.rt)
		}
		conn.Close()
	}
}

func TestParseMajor(t *testing.T) {
	for v, want := range map[string]int{"42.9": 42, "46.0": 46, "50.1": 50, "44.2": 44, "42.beta": 42, " 47 ": 47} {
		if got, err := parseMajor(v); err != nil || got != want {
			t.Errorf("%q: %d %v", v, got, err)
		}
	}
	for _, v := range []string{"", "x.1", "-1.0", "0.1"} {
		if _, err := parseMajor(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

// ShellMajor, Enable and Disable against a shell on a private bus.
func TestShellCalls(t *testing.T) {
	bus := fakeshell.NewBus(t, "")
	sh := fakeshell.Start(t, bus.Addr, fakeshell.Options{Version: "46.0", EnableOK: true})
	c, err := dbus.Connect(bus.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	major, version, err := ShellMajor(ctx, c)
	if err != nil || major != 46 || version != "46.0" {
		t.Fatalf("%d %q %v", major, version, err)
	}
	if ok, err := Enable(ctx, c, UUID); !ok || err != nil {
		t.Fatalf("enable %v %v", ok, err)
	}
	if ok, err := Disable(ctx, c, UUID); ok || err != nil {
		t.Fatalf("disable %v %v", ok, err)
	}
	got := sh.Calls()
	if len(got) != 2 || got[0] != "EnableExtension "+UUID || got[1] != "DisableExtension "+UUID {
		t.Fatalf("%q", got)
	}
	// no shell on the bus: an error, nothing started
	other := fakeshell.NewBus(t, "")
	c2, err := dbus.Connect(other.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, _, err := ShellMajor(ctx, c2); err == nil {
		t.Fatal("a version without a shell")
	}
	if _, err := Enable(ctx, c2, UUID); err == nil {
		t.Fatal("enabled without a shell")
	}
}
