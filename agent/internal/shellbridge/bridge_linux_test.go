//go:build linux

package shellbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/gnomeext"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// rig is a bridge run on a private session bus, its standard input and
// output played by the test, on a fake clock.
type rig struct {
	t     *testing.T
	bus   *fakeshell.Bus
	shell *fakeshell.Shell // nil: no shell
	clk   *clock.Fake
	in    *io.PipeWriter
	lines chan trayproto.Msg
	code  chan int
	data  string // XDG_DATA_HOME
	env   map[string]string
}

type rigOptions struct {
	shell      *fakeshell.Options // nil: no shell on the bus
	enableOnce bool
	bus        *fakeshell.Bus // nil: a new one
	data       string         // XDG_DATA_HOME ("": a new folder)
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	r := &rig{t: t, bus: o.bus, clk: clock.NewFake(time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC)),
		lines: make(chan trayproto.Msg, 64), code: make(chan int, 1), data: o.data}
	if r.data == "" {
		r.data = t.TempDir()
	}
	if r.bus == nil {
		r.bus = fakeshell.NewBus(t, "")
	}
	if o.shell != nil {
		r.shell = fakeshell.Start(t, r.bus.Addr, *o.shell)
	}
	r.env = map[string]string{"DBUS_SESSION_BUS_ADDRESS": r.bus.Addr, "XDG_DATA_HOME": r.data}
	r.run(Options{EnableOnce: o.enableOnce, Getenv: func(k string) string { return r.env[k] }, Home: t.TempDir(), Clock: r.clk})
	return r
}

func (r *rig) run(o Options) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r.in = inW
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		r.code <- Run(ctx, bufio.NewReader(inR), outW, o)
		outW.Close()
	}()
	go func() {
		br := bufio.NewReader(outR)
		for {
			m, err := trayproto.Decode(br)
			if err != nil {
				return
			}
			r.lines <- m
		}
	}()
	r.t.Cleanup(func() {
		inW.Close()
		cancel()
	})
}

// say writes a line of the main process.
func (r *rig) say(m trayproto.Msg) {
	r.t.Helper()
	m.V = trayproto.Version
	must(r.t, trayproto.Encode(r.in, m))
}

func (r *rig) next() trayproto.Msg {
	r.t.Helper()
	select {
	case m := <-r.lines:
		return m
	case <-time.After(5 * time.Second):
		r.t.Fatal("the bridge said nothing")
		return trayproto.Msg{}
	}
}

// none: the bridge says nothing for a while.
func (r *rig) none() {
	r.t.Helper()
	select {
	case m := <-r.lines:
		r.t.Fatalf("the bridge said %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func (r *rig) exit() int {
	r.t.Helper()
	select {
	case c := <-r.code:
		return c
	case <-time.After(10 * time.Second):
		r.t.Fatal("the bridge did not end")
		return -1
	}
}

// owned waits until the bridge owns its name.
func (r *rig) owned() {
	r.t.Helper()
	c := r.conn()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var has bool
		must(r.t, c.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, BusName).Store(&has))
		if has {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("the bridge does not own its name")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// conn is another connection to the bus (not the shell).
func (r *rig) conn() *dbus.Conn {
	r.t.Helper()
	c, err := dbus.Connect(r.bus.Addr)
	must(r.t, err)
	r.t.Cleanup(func() { c.Close() })
	return c
}

// ready waits until the bridge wrote the extension for the shell and
// said the shell is supported (it then serves, the shell wait is over).
func (r *rig) ready(form string) string {
	r.t.Helper()
	dir, err := gnomeext.Dir(func(k string) string { return r.env[k] }, "/nonexistent")
	must(r.t, err)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ok, _ := gnomeext.Current(dir, form); ok {
			break
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("no %s extension in %s", form, dir)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if m := r.next(); m.Type != trayproto.TypeShell || m.Supported == nil || !*m.Supported || m.Attached != nil || m.Why != "" {
		r.t.Fatalf("got %+v, want supported", m)
	}
	return dir
}

// waiter waits until something waits on the fake clock.
func (r *rig) waiter() {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := r.clk.Next(); ok {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("nothing waits on the clock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// call calls the bridge as c's owner would (the shell's connection, or
// another one).
func call(c *dbus.Conn, method string, args ...any) *dbus.Call {
	return c.Object(BusName, ObjectPath).Call(Interface+"."+method, 0, args...)
}

func (r *rig) call(method string, args ...any) *dbus.Call { return call(r.shell.Conn, method, args...) }

func isAttached(t *testing.T, m trayproto.Msg, want bool, why string) {
	t.Helper()
	if m.Type != trayproto.TypeShell || m.Attached == nil || *m.Attached != want || m.Why != why {
		t.Fatalf("got %+v (attached %v), want attached %v why %q", m, m.Attached, want, why)
	}
}

func shell46() *fakeshell.Options { return &fakeshell.Options{Version: "46.0"} }

// attached: a shell that drew the panel, the main process told.
func (r *rig) attached() {
	r.t.Helper()
	var proto uint32
	must(r.t, r.call("Attach", uint32(1), "46.0").Store(&proto))
	if proto != Protocol {
		r.t.Fatalf("Attach answered %d", proto)
	}
	isAttached(r.t, r.next(), true, "")
}

func combinedPanel() *trayproto.Panel {
	p := tray.BuildPanel(panelfixture.Combined())
	p.Serial = 1
	return &p
}

// The bridge owns local.AIUsage.Panel; a newer agent's bridge takes it
// over (the older one ends with 2); quit releases it and ends with 0.
func TestBridgeOwnsTheName(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.owned()
	r2 := newRig(t, rigOptions{shell: nil, bus: r.bus})
	if c := r.exit(); c != exitReplaced {
		t.Fatalf("the older bridge ended with %d", c)
	}
	r2.owned()
	r2.say(trayproto.Msg{Type: trayproto.TypeQuit})
	if c := r2.exit(); c != trayproto.ExitOK {
		t.Fatalf("quit: %d", c)
	}
	var has bool
	must(t, r.conn().BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, BusName).Store(&has))
	if has {
		t.Fatal("the name is still owned")
	}
	// the end of standard input ends it too
	r3 := newRig(t, rigOptions{shell: shell46(), bus: fakeshell.NewBus(t, "")})
	r3.owned()
	r3.in.Close()
	if c := r3.exit(); c != trayproto.ExitOK {
		t.Fatalf("EOF: %d", c)
	}
}

// Calls from anyone but the owner of org.gnome.Shell are refused, and
// nothing reaches the main process (design 6.4).
func TestCallsFromOthersAreRefused(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: combinedPanel()})
	other := r.conn()
	for _, c := range []*dbus.Call{
		call(other, "Attach", uint32(1), "46.0"), call(other, "Click", "r.claude.0"), call(other, "GetView"),
		call(other, "Ping"), call(other, "Detach"),
	} {
		if n := dbusErrorName(c.Err); n != errAccessDenied {
			t.Fatalf("%s answered %v", c.Method, c.Err)
		}
	}
	r.none()
	// the shell itself is heard
	r.attached()
}

func TestAttachReportsAttached(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	must(t, r.call("Attach", uint32(1), "46.0").Err) // again (after the name came back): nothing new
	must(t, r.call("Ping").Err)
	r.none()
	must(t, r.call("Detach").Err)
	r.timerAt(lockSettle) // a lock may be starting (TestDetachJustBeforeTheLockIsHeld)
	r.none()
	r.clk.Advance(lockSettle)
	isAttached(t, r.next(), false, "detach")
	must(t, r.call("Detach").Err) // not attached: nothing
	r.none()
	r.attached()
}

// No Ping or GetView for 90 s: detached, why "ping".
func TestPingLapseDetaches(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(60 * time.Second)
	must(t, r.call("Ping").Err) // alive at +60 s
	r.clk.Advance(89 * time.Second)
	r.none()
	must(t, r.call("GetView").Err) // GetView counts too, at +149 s
	r.clk.Advance(89 * time.Second)
	r.none()
	r.clk.Advance(2 * time.Second)
	isAttached(t, r.next(), false, "ping")
}

// RF2: the shell leaving the bus (a restart, a crash) detaches, why owner.
func TestShellOwnerGoneDetaches(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	r.shell.Leave(t)
	isAttached(t, r.next(), false, "owner")
}

// RF2: a Detach while the screen is locked (GNOME disables the extension)
// is told only once it is unlocked, and not at all when the extension
// attached again before that.
func TestDetachWhileLockedIsHeld(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	must(t, r.call("Detach").Err)
	r.none()
	r.shell.SetLocked(t, false)
	isAttached(t, r.next(), false, "detach")
	// again, and the extension attaches before the unlock
	r.attached()
	r.shell.SetLocked(t, true)
	must(t, r.call("Detach").Err)
	r.none()
	must(t, r.call("Attach", uint32(1), "46.0").Err)
	r.none()
	r.shell.SetLocked(t, false)
	r.none()
}

// Ruling F14: a held Detach ends the attachment and its Ping timer: a lock
// of 10 minutes, then the extension attaches again and the screen
// unlocks: the main process hears nothing at all.
func TestLongLockStartsNothing(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	must(t, r.call("Detach").Err)
	r.none()
	for range 10 {
		r.clk.Advance(time.Minute)
	}
	r.none()
	must(t, r.call("Attach", uint32(1), "46.0").Err)
	r.shell.SetLocked(t, false)
	r.none()
	// and it is attached again: a Ping lapse from now is told
	r.clk.Advance(PingTimeout)
	isAttached(t, r.next(), false, "ping")
}

// A shell the panel does not support: supported false, exit 78 (plan
// PR-10), and no file written.
func TestUnsupportedShell(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "44.2"}, enableOnce: true})
	m := r.next()
	if m.Type != trayproto.TypeShell || m.Supported == nil || *m.Supported || m.Why != "unsupported 44.2" {
		t.Fatalf("%+v", m)
	}
	if c := r.exit(); c != trayproto.ExitUnsupported {
		t.Fatalf("exit %d", c)
	}
	if entries, _ := os.ReadDir(r.data); len(entries) != 0 {
		t.Fatalf("written: %v", entries)
	}
	if calls := r.shell.Calls(); len(calls) != 0 {
		t.Fatalf("calls %q", calls)
	}
}

// stillRuns: the bridge has not ended.
func (r *rig) stillRuns() {
	r.t.Helper()
	select {
	case c := <-r.code:
		r.t.Fatalf("the bridge ended with %d", c)
	case <-time.After(50 * time.Millisecond):
	}
}

// notSupported: the bridge says no supported shell answers, why.
func notSupported(t *testing.T, m trayproto.Msg, why string) {
	t.Helper()
	if m.Type != trayproto.TypeShell || m.Supported == nil || *m.Supported || m.Attached != nil || m.Why != why {
		t.Fatalf("got %+v, want supported false why %q", m, why)
	}
}

// Review A-M1: no GNOME Shell within 30 s: supported false "no shell", so
// the icons come at once, and the bridge waits on (it parks with 78 only
// for an unsupported version): a shell that comes later on the same bus
// (a session bus that outlived a logout gets the next login's shell) is
// served. Quit still ends it at once.
func TestNoShellWithinWait(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.owned()
	r.waiter()
	r.clk.Advance(ShellWait - time.Millisecond)
	r.none()
	r.clk.Advance(time.Millisecond)
	notSupported(t, r.next(), "no shell")
	for range 10 {
		r.clk.Advance(time.Minute)
	}
	r.none()
	r.stillRuns()
	r.shell = fakeshell.Start(t, r.bus.Addr, *shell46())
	r.ready("gnome46")
	r.attached()

	r2 := newRig(t, rigOptions{})
	r2.owned()
	r2.waiter()
	r2.clk.Advance(ShellWait)
	notSupported(t, r2.next(), "no shell")
	r2.say(trayproto.Msg{Type: trayproto.TypeQuit})
	if c := r2.exit(); c != trayproto.ExitOK {
		t.Fatalf("quit: %d", c)
	}
	// the end of the input, and ctx (the agent stopping), too
	r3 := newRig(t, rigOptions{})
	r3.owned()
	r3.waiter()
	r3.clk.Advance(ShellWait)
	notSupported(t, r3.next(), "no shell")
	r3.in.Close()
	if c := r3.exit(); c != trayproto.ExitOK {
		t.Fatalf("EOF: %d", c)
	}
}

// Review A-M1: a shell without a ShellVersion for 30 s: "no shell
// version", and the bridge waits on; another shell on the bus is asked at
// once; one of a version the panel does not support ends it with 78.
func TestNoShellVersionWithinWait(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{}})
	r.owned()
	r.waiter()
	r.clk.Advance(ShellWait)
	notSupported(t, r.next(), "no shell version")
	r.clk.Advance(5 * time.Minute)
	r.none()
	r.stillRuns()
	r.shell.Conn.Close() // that shell ends; the next one has a version
	r.shell = fakeshell.Start(t, r.bus.Addr, fakeshell.Options{Version: "42.9"})
	r.ready("gnome42") // without the clock moving
	r.attached()

	r2 := newRig(t, rigOptions{})
	r2.owned()
	r2.waiter()
	r2.clk.Advance(ShellWait)
	notSupported(t, r2.next(), "no shell")
	fakeshell.Start(t, r2.bus.Addr, fakeshell.Options{Version: "44.2"})
	notSupported(t, r2.next(), "unsupported 44.2")
	if c := r2.exit(); c != trayproto.ExitUnsupported {
		t.Fatalf("exit %d", c)
	}
}

// Review of fix round 3: after the 30 s wait a new shell is asked for its
// version again within a second when its first answer had none (a shell
// that owns its name before its version is there), not after 30 s.
func TestNewShellAfterTheWaitIsAskedAgainSoon(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.owned()
	r.waiter()
	r.clk.Advance(ShellWait)
	notSupported(t, r.next(), "no shell")
	r.shell = fakeshell.Start(t, r.bus.Addr, fakeshell.Options{Version: "46.0", VersionLater: true})
	r.waiter() // the failed ask waits to be asked again
	r.shell.PublishVersion()
	r.clk.Advance(versionRetry)
	r.ready("gnome46")
	r.attached()
}

// A shell that comes on the bus during the wait is served.
func TestShellAfterTheBridge(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.owned()
	r.clk.Advance(20 * time.Second)
	r.shell = fakeshell.Start(t, r.bus.Addr, fakeshell.Options{Version: "50.1"})
	r.ready("gnome46")
	r.clk.Advance(time.Minute)
	r.attached()
}

func TestBusLossExits75(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.bus.Stop()
	if c := r.exit(); c != trayproto.ExitSessionLost {
		t.Fatalf("exit %d", c)
	}
}

// waitsOn waits until something waits on clk.
func waitsOn(t *testing.T, clk *clock.Fake) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := clk.Next(); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing waits on the clock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// RF3: no address and no bus in the runtime folder: the bridge waits for a
// bus (it dials again every dialRetry: the lost-bus ruling of the 4b
// re-review), ends with 0 at the end of its input, and no bus is ever
// started (a dbus-launch first in PATH records any run).
func TestBridgeNeverAutolaunches(t *testing.T) {
	// A bridge broken to use the D-Bus library's own lookup would also find
	// the user's bus at /run/user/<uid>/bus, whatever XDG_RUNTIME_DIR says:
	// on a PC with such a bus the live part is left to CI runners.
	fakeshell.SkipOnAUserBus(t)
	bin := t.TempDir()
	rec := filepath.Join(t.TempDir(), "ran")
	must(t, os.WriteFile(filepath.Join(bin, "dbus-launch"), []byte("#!/bin/sh\necho \"$@\" >> "+rec+"\nexit 1\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	rt := fakeshell.ShortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", rt)
	for _, env := range []map[string]string{
		{"XDG_RUNTIME_DIR": rt},
		{"XDG_RUNTIME_DIR": rt, "DBUS_SESSION_BUS_ADDRESS": "autolaunch:"},
		{},
	} {
		clk := clock.NewFake(time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC))
		inR, inW := io.Pipe()
		code := make(chan int, 1)
		go func() {
			code <- Run(context.Background(), bufio.NewReader(inR), io.Discard,
				Options{Getenv: func(k string) string { return env[k] }, Home: t.TempDir(), Clock: clk})
		}()
		for range 3 {
			waitsOn(t, clk)
			clk.Advance(dialRetry)
		}
		select {
		case c := <-code:
			t.Fatalf("%v: ended with %d while waiting for a bus", env, c)
		case <-time.After(50 * time.Millisecond):
		}
		inW.Close()
		select {
		case c := <-code:
			if c != trayproto.ExitOK {
				t.Fatalf("%v: exit %d", env, c)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%v: no end", env)
		}
	}
	if b, err := os.ReadFile(rec); err == nil {
		t.Fatalf("dbus-launch ran: %s", b)
	}
}

// The extension's files for the running shell are written when missing
// or stale (design 6.6), and left alone when current.
func TestWritesMissingOrStaleExtension(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	dir := r.ready("gnome46")
	r.say(trayproto.Msg{Type: trayproto.TypeQuit})
	r.exit()
	// stale: a file changed; the next start writes it again
	must(t, os.WriteFile(filepath.Join(dir, "view.js"), []byte("old"), 0o644))
	r2 := newRig(t, rigOptions{shell: nil, bus: r.bus, data: r.data})
	r2.ready("gnome46")
	// GNOME 42: its own form
	r3 := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "42.9"}})
	r3.ready("gnome42")
	if ok, _ := gnomeext.Current(filepath.Join(r3.data, "gnome-shell", "extensions", gnomeext.UUID), "gnome46"); ok {
		t.Fatal("the 46 form on GNOME 42")
	}
}

// Enable once (design 6.6): one EnableExtension; true → enabled; false →
// no message; without EnableOnce no call.
func TestEnableOnce(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableOK: true}, enableOnce: true})
	r.ready("gnome46")
	if m := r.next(); m.Type != trayproto.TypeShell || !m.Enabled {
		t.Fatalf("%+v", m)
	}
	r.none()
	if calls := r.shell.Calls(); !slices.Equal(calls, []string{"EnableExtension " + gnomeext.UUID}) {
		t.Fatalf("%q", calls)
	}
	r2 := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableOK: false}, enableOnce: true})
	r2.ready("gnome46")
	deadline := time.Now().Add(5 * time.Second)
	for len(r2.shell.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls := r2.shell.Calls(); len(calls) != 1 {
		t.Fatalf("%q", calls)
	}
	r2.none()
	r3 := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableOK: true}})
	r3.ready("gnome46")
	r3.none()
	if calls := r3.shell.Calls(); len(calls) != 0 {
		t.Fatalf("%q", calls)
	}
}

// Review A-I1: an EnableExtension that answers an error, or no answer in
// time, may have turned the extension on all the same: it counts as done
// (the Enabled line: the main process writes gnome-panel-enabled and no
// later start asks again, so the user's own "off" is never undone). Only a
// definite (false,) leaves it to the next start (TestEnableOnce).
func TestEnableWithoutADefiniteNoIsDone(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableFails: true}, enableOnce: true})
	r.ready("gnome46")
	if m := r.next(); m.Type != trayproto.TypeShell || !m.Enabled {
		t.Fatalf("an Enable that failed: %+v", m)
	}
	r.none()

	g := fakeshell.NewGate(t)
	old := enableTimeout
	enableTimeout = 100 * time.Millisecond // read once by Run
	r2 := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableOK: true, EnableGate: g}, enableOnce: true})
	r2.ready("gnome46")
	enableTimeout = old
	waitPending(t, g, 1)
	if m := r2.next(); m.Type != trayproto.TypeShell || !m.Enabled {
		t.Fatalf("an Enable not answered in time: %+v", m)
	}
	r2.none()
	if calls := r2.shell.Calls(); !slices.Equal(calls, []string{"EnableExtension " + gnomeext.UUID}) {
		t.Fatalf("%q", calls)
	}
}

// Review of fix round 3: an EnableExtension that never reached the shell
// (the shell left the bus between its version and the call: the bus
// answers ServiceUnknown or NameHasNoOwner) is not done: no Enabled line,
// so the next start asks again.
func TestEnableNeverDeliveredIsNotDone(t *testing.T) {
	left := make(chan struct{})
	var once sync.Once
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableOK: true, OnVersion: func(s *fakeshell.Shell) {
		// gone before the version is answered; on the bus's handler
		// goroutine, so no t.Fatal here (review of stage 5a M5)
		once.Do(func() {
			if _, err := s.Conn.ReleaseName(fakeshell.ShellName); err != nil {
				t.Error(err)
			}
			close(left)
		})
	}}, enableOnce: true})
	<-left
	r.ready("gnome46")
	r.none()
	if calls := r.shell.Calls(); len(calls) != 0 {
		t.Fatalf("the shell got %q", calls)
	}
}

// The classification of an Enable's result: done (the Enabled line; why
// "unclear" when only an error came, so the main process does not log it
// as turned on) or not (the next start asks again).
func TestEnableOutcome(t *testing.T) {
	writeErr := &net.OpError{Op: "write", Net: "unix", Err: syscall.EPIPE}
	readErr := &net.OpError{Op: "read", Net: "unix", Err: syscall.ECONNRESET}
	for _, c := range []struct {
		name string
		ok   bool
		err  error
		done bool
		why  string
	}{
		{"turned on", true, nil, true, ""},
		{"a definite no", false, nil, false, ""},
		{"no owner", false, dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}, false, ""},
		{"no owner, no auto-start", false, &dbus.Error{Name: "org.freedesktop.DBus.Error.NameHasNoOwner"}, false, ""},
		{"closed before it was sent", false, dbus.ErrClosed, false, ""},
		{"a failed write", false, writeErr, false, ""},
		{"the shell's error", false, dbus.Error{Name: "org.freedesktop.DBus.Error.NoReply"}, true, "unclear"},
		{"no answer in time", false, context.DeadlineExceeded, true, "unclear"},
		{"the connection broke while waiting", false, readErr, true, "unclear"},
		{"EOF while waiting", false, io.EOF, true, "unclear"},
	} {
		done, why := enableOutcome(c.ok, c.err)
		if done != c.done || why != c.why {
			t.Errorf("%s: done %v why %q, want %v %q", c.name, done, why, c.done, c.why)
		}
	}
}

// Review of fix round 3: an Enable result that is waiting already when the
// bridge is told to quit is still reported (the main process writes its
// marker; without it the next start would turn the extension on again).
func TestEnableResultWaitingAtQuitIsTold(t *testing.T) {
	for _, r := range []enableResult{{done: true}, {done: true, why: "unclear"}} {
		var out bytes.Buffer
		b := &bridge{out: &out}
		ch := make(chan enableResult, 1)
		ch <- r
		b.leaving(ch)
		m, err := trayproto.Decode(bufio.NewReader(&out))
		if err != nil || m.Type != trayproto.TypeShell || !m.Enabled || m.Why != r.why {
			t.Fatalf("%+v: told %+v (%v)", r, m, err)
		}
	}
	var out bytes.Buffer
	b := &bridge{out: &out}
	ch := make(chan enableResult, 1)
	ch <- enableResult{}
	b.leaving(ch)
	b.leaving(make(chan enableResult, 1)) // nothing waiting: nothing told, at once
	if out.Len() != 0 {
		t.Fatalf("told %q", out.String())
	}
}

// RF1: an older or newer extension's calls keep working: Attach with
// protocol 0 or 1, Protocol 1, and a GetView with every key of design 6.4.
func TestOldExtensionCallsStillWork(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	for _, proto := range []uint32{0, 1, 2} {
		var got uint32
		must(t, r.call("Attach", proto, "42.9").Store(&got))
		if got != 1 {
			t.Fatalf("Attach(%d) answered %d", proto, got)
		}
	}
	isAttached(t, r.next(), true, "")
	v, err := r.shell.Conn.Object(BusName, ObjectPath).GetProperty(Interface + ".Protocol")
	must(t, err)
	if v.Value() != uint32(1) {
		t.Fatalf("Protocol %v", v.Value())
	}
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: combinedPanel()})
	var view string
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(view, `"serial":1`) && time.Now().Before(deadline) {
		must(t, r.call("GetView").Store(&view))
	}
	var top map[string]json.RawMessage
	must(t, json.Unmarshal([]byte(view), &top))
	keys := func(raw json.RawMessage, want ...string) {
		t.Helper()
		var m map[string]json.RawMessage
		must(t, json.Unmarshal(raw, &m))
		for _, k := range want {
			if _, ok := m[k]; !ok {
				t.Fatalf("no %q in %s", k, raw)
			}
		}
	}
	keys([]byte(view), "serial", "new_version", "sections", "prompt", "server", "actions", "top")
	var sections []json.RawMessage
	must(t, json.Unmarshal(top["sections"], &sections))
	keys(sections[0], "provider", "name", "account", "updated", "problem", "stale", "rows")
	var sec struct{ Rows []json.RawMessage }
	must(t, json.Unmarshal(sections[0], &sec))
	keys(sec.Rows[0], "id", "label", "percent", "percent_text", "detail", "level", "more")
	var acts, tops []json.RawMessage
	must(t, json.Unmarshal(top["actions"], &acts))
	must(t, json.Unmarshal(top["top"], &tops))
	keys(acts[0], "id", "text", "enabled", "visible", "checkbox", "checked")
	keys(tops[0], "provider", "text", "percent", "level", "stale")
	// introspection names the interface, as the extension knows it
	var xml string
	must(t, r.shell.Conn.Object(BusName, ObjectPath).Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml))
	if !strings.Contains(xml, `<interface name="local.AIUsage.Panel1">`) || !strings.Contains(xml, `<signal name="ViewChanged">`) {
		t.Fatalf("%s", xml)
	}
}

// GetView's JSON is the panel's, names fixed by the golden file (Task 5);
// before the first view it is an empty panel.
func TestGetViewJSONNames(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	var view string
	must(t, r.call("GetView").Store(&view))
	if view != `{"serial":0,"new_version":"","sections":[],"prompt":"","server":"","actions":[],"top":[]}` {
		t.Fatalf("before the first view: %s", view)
	}
	p := tray.BuildPanel(panelfixture.Combined())
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: &p})
	golden, err := os.ReadFile("../tray/testdata/panel-combined.json")
	must(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for view != string(bytes.TrimSpace(golden)) {
		if time.Now().After(deadline) {
			t.Fatalf("GetView:\n%s\nwant:\n%s", view, golden)
		}
		must(t, r.call("GetView").Store(&view))
	}
}

// RF4 and PR-15: a click on a row the current view does not have, on a
// disabled or hidden action, or on the new-version line without one, is
// refused and goes nowhere; a known one reaches the main process.
func TestClickOnlyKnownIDs(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	if n := dbusErrorName(r.call("Click", "r.claude.0").Err); n != ErrUnknownItem {
		t.Fatalf("before any view: %q", n)
	}
	p := combinedPanel()
	p.Sections[1].Rows = p.Sections[1].Rows[:2] // Codex lost its rows from r.codex.2 on
	for i := range p.Actions {
		switch p.Actions[i].ID {
		case "refresh":
			p.Actions[i].Enabled = false
		case "dashboard":
			p.Actions[i].Visible = false
		}
	}
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: p})
	// the view arrived once a known row is clickable
	deadline := time.Now().Add(5 * time.Second)
	for r.call("Click", "r.codex.1").Err != nil {
		if time.Now().After(deadline) {
			t.Fatal("the view did not arrive")
		}
	}
	if m := r.next(); m.Type != trayproto.TypeClick || m.ID != "r.codex.1" {
		t.Fatalf("%+v", m)
	}
	for _, id := range []string{"r.codex.3", "r.codex.2", "refresh", "dashboard", "newversion", "h.claude", "server", "p.codex", ""} {
		if n := dbusErrorName(r.call("Click", id).Err); n != ErrUnknownItem {
			t.Fatalf("Click(%q): %q", id, n)
		}
	}
	r.none()
	for _, id := range []string{"r.claude.0", "quit", "autostart", "remove"} {
		must(t, r.call("Click", id).Err)
		if m := r.next(); m.Type != trayproto.TypeClick || m.ID != id {
			t.Fatalf("%+v", m)
		}
	}
	p2 := combinedPanel()
	p2.Serial = 2
	p2.NewVersion = "New version available: 0.9.2"
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: p2})
	for r.call("Click", "newversion").Err != nil {
		if time.Now().After(deadline) {
			t.Fatal("newversion refused")
		}
	}
	if m := r.next(); m.ID != "newversion" {
		t.Fatalf("%+v", m)
	}
}

// A view with a new serial is signalled (ViewChanged); the same serial
// again is not.
func TestViewChangedSignal(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	c := r.shell.Conn
	must(t, c.AddMatchSignal(dbus.WithMatchInterface(Interface), dbus.WithMatchMember("ViewChanged")))
	sigs := make(chan *dbus.Signal, 8)
	c.Signal(sigs)
	wantSignal := func(n uint64) {
		t.Helper()
		for {
			select {
			case s := <-sigs:
				if s.Name != Interface+".ViewChanged" {
					continue
				}
				if len(s.Body) != 1 || s.Body[0] != n || s.Path != ObjectPath {
					t.Fatalf("%+v", s)
				}
				return
			case <-time.After(5 * time.Second):
				t.Fatalf("no ViewChanged %d", n)
			}
		}
	}
	p := combinedPanel()
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: p})
	wantSignal(1)
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: p})
	p2 := *p
	p2.Serial = 2
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: &p2})
	wantSignal(2) // and not 1 again before it
	var view string
	must(t, r.call("GetView").Store(&view))
	if !strings.HasPrefix(view, `{"serial":2,`) {
		t.Fatalf("%s", view)
	}
}

// The running notice is shown through org.freedesktop.Notifications.
func TestNoticeViaNotifications(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.say(trayproto.Msg{Type: trayproto.TypeNotice, Text: "AI Usage is running. Its icon is in the top bar."})
	deadline := time.Now().Add(5 * time.Second)
	for len(r.shell.Notices()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no notification")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.shell.Notices(); !slices.Equal(got, []string{"AI Usage|AI Usage|AI Usage is running. Its icon is in the top bar."}) {
		t.Fatalf("%q", got)
	}
}

// Review I1: the bridge says once that the shell is supported, as its own
// line (the main process removes the marker only after it), before it
// turns the extension on.
func TestSupportedLine(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", EnableOK: true}, enableOnce: true})
	var b bytes.Buffer
	must(t, trayproto.Encode(&b, r.next()))
	if b.String() != `{"v":1,"type":"shell","supported":true}`+"\n" {
		t.Fatalf("%s", b.String())
	}
	if m := r.next(); !m.Enabled {
		t.Fatalf("%+v", m)
	}
	r.none()
}

// Review I2: after a Ping lapse the caller is kept as lapsed: its next
// Ping or GetView attaches it again (design 6.4: attached is the caller on
// the bus and a call under 90 s old).
func TestLapsedCallerAttachesAgain(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	for _, method := range []string{"Ping", "GetView"} {
		r.clk.Advance(PingTimeout)
		isAttached(t, r.next(), false, "ping")
		r.clk.Advance(time.Hour) // the shell stalled
		r.none()
		must(t, r.call(method).Err)
		isAttached(t, r.next(), true, "")
		must(t, r.call("Ping").Err)
		r.none()
	}
	// attached again for good: a lapse from now is told again
	r.clk.Advance(PingTimeout)
	isAttached(t, r.next(), false, "ping")
}

// Review I2: a lapsed caller that leaves the bus is forgotten: the same
// connection taking the shell's name again is not attached by its Ping.
func TestLapsedCallerThatLeftIsForgotten(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(PingTimeout)
	isAttached(t, r.next(), false, "ping")
	r.shell.Leave(t)
	isAttached(t, r.next(), false, "owner") // no supported shell answers now (review H1)
	reply, err := r.shell.Conn.RequestName(fakeshell.ShellName, dbus.NameFlagDoNotQueue)
	must(t, err)
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName: %v", reply)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.call("Ping").Err != nil { // the bridge saw the owner come back
		if time.Now().After(deadline) {
			t.Fatal("Ping refused")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.none()
	r.attached() // an Attach does
}

// unicast sends a signal from c to dest only (not through match rules).
func unicast(t *testing.T, c *dbus.Conn, dest string, path dbus.ObjectPath, iface, member string, body ...any) {
	t.Helper()
	m := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath: dbus.MakeVariant(path), dbus.FieldInterface: dbus.MakeVariant(iface),
		dbus.FieldMember: dbus.MakeVariant(member), dbus.FieldDestination: dbus.MakeVariant(dest),
		dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(body...)),
	}, Body: body}
	must(t, c.Send(m, nil).Err)
}

// Review m1: the bus's signals count only from the bus, ActiveChanged only
// from the screen saver: another program's NameLost does not end the
// bridge, its NameOwnerChanged does not detach, its ActiveChanged does not
// tell a held detach.
func TestSignalsFromOthersAreIgnored(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	other := r.conn()
	var me string
	must(t, other.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, BusName).Store(&me))
	unicast(t, other, me, "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameLost", BusName)
	unicast(t, other, me, "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameOwnerChanged", shellName, r.shell.Conn.Names()[0], "")
	r.none()
	select {
	case c := <-r.code:
		t.Fatalf("the bridge ended with %d", c)
	default:
	}
	must(t, r.call("Ping").Err) // still attached, still serving
	must(t, r.call("Detach").Err)
	r.none() // held: locked
	unicast(t, other, me, screenSaverPath, screenSaverIface, "ActiveChanged", false)
	must(t, other.Emit(screenSaverPath, screenSaverIface+".ActiveChanged", false))
	r.none()
	r.shell.SetLocked(t, false)
	isAttached(t, r.next(), false, "detach")
}

// waitPending waits until n GetActive calls wait at g.
func waitPending(t *testing.T, g *fakeshell.ActiveGate, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for g.Pending() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d GetActive calls wait, want %d", g.Pending(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Review m6: the screen saver's answer to a Detach that an Attach ended
// before the answer came tells nothing.
func TestLockAnswerAfterReattachTellsNothing(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	g := r.shell.HoldGetActive(t)
	must(t, r.call("Detach").Err)
	waitPending(t, g, 1) // it will answer "not locked"
	must(t, r.call("Attach", uint32(1), "46.0").Err)
	g.Open()
	r.none()
}

// Review m2: an old GetActive answer does not apply to a newer held
// Detach: Detach while unlocked (its answer pending), Attach, lock, Detach
// again; the first answer ("not locked") tells nothing during the lock.
func TestOldLockAnswerIsIgnored(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	g := r.shell.HoldGetActive(t)
	must(t, r.call("Detach").Err)
	waitPending(t, g, 1)
	must(t, r.call("Attach", uint32(1), "46.0").Err)
	r.shell.SetLocked(t, true)
	must(t, r.call("Detach").Err)
	waitPending(t, g, 2)
	g.Open()
	r.none()
	r.shell.SetLocked(t, false)
	isAttached(t, r.next(), false, "detach")
}

// timerAt waits until something waits on the fake clock until d from now.
func (r *rig) timerAt(d time.Duration) {
	r.t.Helper()
	want := r.clk.Now().Add(d)
	deadline := time.Now().Add(5 * time.Second)
	for !r.clk.Waiting(want) {
		if time.Now().After(deadline) {
			r.t.Fatalf("nothing waits until %v", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Review m3: a held Detach asks the screen saver again every 30 s: no
// longer active (its signal never came), the Detach is told. (The screen
// saver leaving the bus tells it at once: TestHeldDetachIsToldWhenTheLock
// ServiceGoes.)
func TestHeldDetachAsksAgain(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(7 * time.Second) // timers of their own (none ends at a recheck)
	must(t, r.call("Detach").Err)
	r.timerAt(lockRecheck)
	r.clk.Advance(lockRecheck) // still locked: held
	r.timerAt(lockRecheck)
	r.none()
	r.shell.SetLockedQuietly(false)
	r.clk.Advance(lockRecheck - time.Millisecond)
	r.none()
	r.clk.Advance(time.Millisecond)
	isAttached(t, r.next(), false, "detach")
}

// Review A-M6: a detach held for a lock is told as soon as the screen
// saver leaves the bus (no lock service, no lock), not at the next
// recheck: why "detach" while the shell stays; "owner" when the shell's
// connection went (its names go together, in an order of the bus's
// choosing), and then the shell's leaving is not told a second time.
func TestHeldDetachIsToldWhenTheLockServiceGoes(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(7 * time.Second)
	must(t, r.call("Detach").Err)
	r.timerAt(lockRecheck)
	r.none()
	r.shell.Release(t, fakeshell.ScreenShieldName)
	isAttached(t, r.next(), false, "detach")
	r.none()

	r2 := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r2.ready("gnome46")
	r2.attached()
	r2.clk.Advance(7 * time.Second)
	must(t, r2.call("Detach").Err)
	r2.timerAt(lockRecheck)
	r2.none()
	r2.shell.Conn.Close() // the session ends during the lock
	isAttached(t, r2.next(), false, "owner")
	r2.none()
}

// Measured on GNOME 46 and 50 (CI panel.lock; the headless 42 of the
// runner has no screen lock): a lock disables the
// extensions (its Detach) when it starts, and the screen saver says it is
// active only after the lock screen's animation, so GetActive answers
// false to that Detach (GNOME's screenShield.js: ActiveChanged only from
// lockScreenShown). A Detach whose first answer is "not active" is held
// for lockSettle: ActiveChanged(true) meanwhile, or "active" when asked
// again, keeps it held for the lock; else it is told then.
func TestDetachJustBeforeTheLockIsHeld(t *testing.T) {
	for _, how := range []string{"signal", "asked again"} {
		r := newRig(t, rigOptions{shell: shell46()})
		r.ready("gnome46")
		r.attached()
		must(t, r.call("Detach").Err)
		r.timerAt(lockSettle)
		r.clk.Advance(lockSettle / 2)
		if how == "signal" {
			r.shell.SetLocked(t, true)
			r.timerAt(lockRecheck) // held for the lock at once, not at the settle's end
		} else {
			r.shell.SetLockedQuietly(true)
		}
		r.none()
		r.clk.Advance(lockSettle)
		r.none()
		for range 10 { // a lock of 10 minutes, asked again every 30 s
			r.clk.Advance(time.Minute)
		}
		r.none()
		r.shell.SetLocked(t, false)
		isAttached(t, r.next(), false, "detach")
	}
}

// Without a lock the Detach is told when lockSettle has passed, not
// before; the extension attaching again meanwhile tells nothing. A shell
// that left (why owner) is told at once, as before.
func TestDetachWithoutALockIsToldAfterTheSettle(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	must(t, r.call("Detach").Err)
	r.timerAt(lockSettle)
	r.clk.Advance(lockSettle - time.Millisecond)
	r.none()
	r.clk.Advance(time.Millisecond)
	isAttached(t, r.next(), false, "detach")

	r.attached()
	must(t, r.call("Detach").Err)
	r.timerAt(lockSettle)
	must(t, r.call("Attach", uint32(1), "46.0").Err)
	r.clk.Advance(lockSettle)
	r.none()

	r.shell.Leave(t)
	isAttached(t, r.next(), false, "owner")
}

// Review H1: once the bridge said a supported shell answers, the shell
// leaving the bus is told even with no extension attached (it detached
// before, or never attached): the main process then knows that no
// supported shell answers and keeps the gnome-panel-attached marker
// (design 6.4: the marker is not removed when a session ends). Before
// supported:true nothing is told.
func TestShellLeavingIsToldWithoutACaller(t *testing.T) {
	// the extension detached first (the user turned it off), then the
	// shell went
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	must(t, r.call("Detach").Err)
	r.timerAt(lockSettle)
	r.clk.Advance(lockSettle)
	isAttached(t, r.next(), false, "detach")
	r.shell.Leave(t)
	isAttached(t, r.next(), false, "owner")
	r.none()
	// never attached
	r2 := newRig(t, rigOptions{shell: shell46()})
	r2.ready("gnome46")
	r2.shell.Leave(t)
	isAttached(t, r2.next(), false, "owner")
	// no version yet: not supported yet, nothing told
	r3 := newRig(t, rigOptions{shell: &fakeshell.Options{}})
	r3.owned()
	r3.waiter()
	r3.shell.Leave(t)
	r3.none()
}

// Review H1: a detach held for a lock and told after the shell left (the
// session ended during the lock) says why "owner".
func TestHeldDetachAfterTheShellLeftSaysOwner(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(7 * time.Second) // timers of their own (none ends at a recheck)
	must(t, r.call("Detach").Err)
	r.timerAt(lockRecheck)
	r.none()
	r.shell.Leave(t)
	r.none()
	r.shell.Release(t, fakeshell.ScreenShieldName) // told now (review A-M6)
	isAttached(t, r.next(), false, "owner")
	r.none()
}

// Review H2: while a detach is held for a lock, a GetActive that fails is
// not taken as "unlocked": it is asked again at the next recheck; an
// answer starts the count again; three failures in a row tell the detach.
func TestFailedLockAskIsAskedAgain(t *testing.T) {
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(7 * time.Second)
	r.shell.FailGetActive(1) // the Detach's own ask fails
	must(t, r.call("Detach").Err)
	r.timerAt(lockRecheck)
	r.none()
	r.clk.Advance(lockRecheck) // locked
	r.timerAt(lockRecheck)
	r.none()
	r.shell.FailGetActive(2)
	for range 3 { // fails, fails, locked
		r.clk.Advance(lockRecheck)
		r.timerAt(lockRecheck)
	}
	r.none()
	r.shell.FailGetActive(3)
	for range 2 {
		r.clk.Advance(lockRecheck)
		r.timerAt(lockRecheck)
	}
	r.none()
	r.clk.Advance(lockRecheck) // the third failure in a row
	isAttached(t, r.next(), false, "detach")
	if n := r.shell.GetActives(); n != 8 {
		t.Fatalf("%d GetActive calls, want 8", n)
	}
}

// Review H2: a GetActive that is not answered in time counts as a failed
// ask too (a busy shell during a real lock).
func TestUnansweredLockAskIsAskedAgain(t *testing.T) {
	old := lockAskTimeout
	lockAskTimeout = 100 * time.Millisecond // read once by Run, before its first line
	r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
	r.ready("gnome46")
	lockAskTimeout = old
	r.attached()
	r.clk.Advance(7 * time.Second)
	g := r.shell.HoldGetActive(t)
	must(t, r.call("Detach").Err)
	waitPending(t, g, 1)
	r.timerAt(lockRecheck) // timed out: asked again then
	r.none()
	r.clk.Advance(lockRecheck)
	waitPending(t, g, 2)
	r.timerAt(lockRecheck)
	r.none()
	r.clk.Advance(lockRecheck)
	waitPending(t, g, 3)
	isAttached(t, r.next(), false, "detach")
}

// Review H4: a Detach from the extension whose Pings lapsed forgets it:
// its later Ping or GetView attaches nothing; its Attach does.
func TestDetachFromALapsedCallerForgetsIt(t *testing.T) {
	r := newRig(t, rigOptions{shell: shell46()})
	r.ready("gnome46")
	r.attached()
	r.clk.Advance(PingTimeout)
	isAttached(t, r.next(), false, "ping")
	must(t, r.call("Detach").Err)
	must(t, r.call("Ping").Err)
	must(t, r.call("GetView").Err)
	r.none()
	r.attached()
}

// Lost-bus ruling (4b re-review): a bridge started while its session bus
// is gone (a logout the agent outlived) waits for it in the process, as
// the icon helpers do, instead of ending with 75 again and again (five of
// those stop it for minutes): it dials again every dialRetry, keeps the
// views it gets meanwhile, and serves the bus that comes back at its
// address.
func TestBridgeWaitsForItsBus(t *testing.T) {
	fakeshell.SkipOnAUserBus(t) // a broken dial would find the user's bus
	addr := fakeshell.SocketIn(fakeshell.ShortDir(t))
	r := &rig{t: t, clk: clock.NewFake(time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC)),
		lines: make(chan trayproto.Msg, 64), code: make(chan int, 1), data: t.TempDir()}
	r.env = map[string]string{"DBUS_SESSION_BUS_ADDRESS": addr, "XDG_RUNTIME_DIR": fakeshell.ShortDir(t), "XDG_DATA_HOME": r.data}
	r.run(Options{Getenv: func(k string) string { return r.env[k] }, Home: t.TempDir(), Clock: r.clk})
	for range 5 {
		r.waiter()
		r.clk.Advance(dialRetry)
	}
	r.none()
	select {
	case c := <-r.code:
		t.Fatalf("ended with %d while waiting for its bus", c)
	default:
	}
	r.say(trayproto.Msg{Type: trayproto.TypeView, Panel: combinedPanel()})
	r.bus = fakeshell.NewBus(t, addr)
	r.shell = fakeshell.Start(t, addr, *shell46())
	r.waiter()
	r.clk.Advance(dialRetry)
	r.ready("gnome46")
	var view string
	must(t, r.call("GetView").Store(&view))
	if !strings.HasPrefix(view, `{"serial":1,`) {
		t.Fatalf("the view got while waiting: %s", view)
	}
	r.attached()
}
