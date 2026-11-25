//go:build linux

package shellbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func dbusErrorName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return ""
}

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

// No GNOME Shell within 30 s: supported false, exit 78.
func TestNoShellWithinWait(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.owned()
	r.waiter()
	r.clk.Advance(ShellWait - time.Millisecond)
	r.none()
	r.clk.Advance(time.Millisecond)
	m := r.next()
	if m.Supported == nil || *m.Supported || m.Why != "no shell" {
		t.Fatalf("%+v", m)
	}
	if c := r.exit(); c != trayproto.ExitUnsupported {
		t.Fatalf("exit %d", c)
	}
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

// RF3: no address and no bus in the runtime folder: exit 75, and no bus is
// ever started (a dbus-launch first in PATH records any run).
func TestBridgeNeverAutolaunches(t *testing.T) {
	// A bridge broken to use the D-Bus library's own lookup would also find
	// the user's bus at /run/user/<uid>/bus, whatever XDG_RUNTIME_DIR says:
	// on a PC with such a bus the live part is left to CI runners.
	if _, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid())); err == nil && os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("this PC has a user session bus: the live part runs on CI runners only")
	}
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
		code := make(chan int, 1)
		go func() {
			code <- Run(context.Background(), bufio.NewReader(strings.NewReader("")), io.Discard,
				Options{Getenv: func(k string) string { return env[k] }, Home: t.TempDir()})
		}()
		select {
		case c := <-code:
			if c != trayproto.ExitSessionLost {
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
	r.none()
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
	unicast(t, other, me, screenSaverPath, screenSaverName, "ActiveChanged", false)
	must(t, other.Emit(screenSaverPath, screenSaverName+".ActiveChanged", false))
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
// longer active (its signal never came), or it cannot be asked (gone from
// the bus), the Detach is told.
func TestHeldDetachAsksAgain(t *testing.T) {
	for _, end := range []string{"quiet unlock", "screen saver gone"} {
		r := newRig(t, rigOptions{shell: &fakeshell.Options{Version: "46.0", Locked: true}})
		r.ready("gnome46")
		r.attached()
		r.clk.Advance(7 * time.Second) // timers of their own (none ends at a recheck)
		must(t, r.call("Detach").Err)
		r.timerAt(lockRecheck)
		r.clk.Advance(lockRecheck) // still locked: held
		r.timerAt(lockRecheck)
		r.none()
		if end == "quiet unlock" {
			r.shell.SetLockedQuietly(false)
		} else {
			r.shell.Release(t, fakeshell.ScreenSaverName)
		}
		r.clk.Advance(lockRecheck - time.Millisecond)
		r.none()
		r.clk.Advance(time.Millisecond)
		m := r.next()
		if m.Attached == nil || *m.Attached || m.Why != "detach" {
			t.Fatalf("%s: %+v", end, m)
		}
	}
}
