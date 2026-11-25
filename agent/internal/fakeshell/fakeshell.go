//go:build linux

// Package fakeshell stands in for a GNOME session in tests: a private
// session bus (dbus-daemon) and a connection that plays GNOME Shell on it
// (org.gnome.Shell with its ShellVersion, org.gnome.Shell.Extensions that
// records EnableExtension and DisableExtension, org.gnome.ScreenSaver,
// org.freedesktop.Notifications that records Notify). Tests only: nothing
// that ships imports it.
package fakeshell

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// Bus is a private session bus.
type Bus struct {
	Addr string
	cmd  *exec.Cmd
}

// Daemon is the path of dbus-daemon. Without one the test is skipped, but
// on CI (GITHUB_ACTIONS=true) it fails: a runner without dbus-daemon would
// otherwise pass every bus test by skipping it (review I3).
func Daemon(t testing.TB) string {
	t.Helper()
	daemon, err := lookDaemon()
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("no dbus-daemon on this CI runner: the session bus tests cannot run")
		}
		t.Skip("no dbus-daemon")
	}
	return daemon
}

var (
	daemonMu    sync.Mutex
	daemonFound string
)

// lookDaemon finds dbus-daemon on PATH and remembers it (NoBus empties
// PATH, and a test may start a bus after that).
func lookDaemon() (string, error) {
	daemonMu.Lock()
	defer daemonMu.Unlock()
	if daemonFound != "" {
		return daemonFound, nil
	}
	p, err := exec.LookPath("dbus-daemon")
	if err == nil {
		daemonFound = p
	}
	return p, err
}

// NewBus starts a private session bus, at addr ("unix:path=…") or at an
// address of its own (""); without dbus-daemon the test is skipped (it
// fails on CI: Daemon). It is stopped when the test ends.
func NewBus(t testing.TB, addr string) *Bus {
	t.Helper()
	daemon := Daemon(t)
	args := []string{"--session", "--nofork", "--print-address=1"}
	if addr != "" {
		args = append(args, "--address="+addr)
	}
	cmd := exec.Command(daemon, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b := &Bus{cmd: cmd}
	t.Cleanup(b.Stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	b.Addr = strings.TrimSpace(line)
	return b
}

// Stop ends the bus (every connection to it is lost).
func (b *Bus) Stop() {
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_, _ = b.cmd.Process.Wait()
	}
}

// SocketIn is "unix:path=<dir>/bus", the address of a bus in dir as in
// $XDG_RUNTIME_DIR (a short dir: socket paths are limited to 108 bytes).
func SocketIn(dir string) string { return "unix:path=" + filepath.Join(dir, "bus") }

// ShortDir is a new empty folder with a short path, removed when the test
// ends.
func ShortDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("", "fs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// NoBus prepares a test of a process without a session bus (plan RF3: an
// SSH login, "su -", before the first GNOME login): no
// DBUS_SESSION_BUS_ADDRESS, $XDG_RUNTIME_DIR an empty folder, and PATH
// holding nothing but recorders for dbus-launch (which the D-Bus library
// runs to start a bus when it finds none) and gio. It returns the runtime
// folder and ran: what a recorder was run with ("" when it was not).
// The D-Bus library's own lookup also finds the user's bus at
// /run/user/<uid>/bus whatever XDG_RUNTIME_DIR says, so a broken build
// would dial it: on a PC with such a bus the test is skipped, unless
// GITHUB_ACTIONS=true (CI runners, a container and a sandbox run it).
func NoBus(t testing.TB) (runtimeDir string, ran func(prog string) string) {
	t.Helper()
	if _, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid())); err == nil && os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("this PC has a user session bus: this test runs on CI runners only")
	}
	_, _ = lookDaemon() // before PATH is emptied
	bin, rec := t.TempDir(), t.TempDir()
	for _, prog := range []string{"dbus-launch", "gio"} {
		script := "#!/bin/sh\necho \"$@\" >> " + filepath.Join(rec, prog) + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, prog), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	runtimeDir = ShortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	return runtimeDir, func(prog string) string {
		b, _ := os.ReadFile(filepath.Join(rec, prog))
		return string(b)
	}
}

// The names and paths the shell answers on.
const (
	ShellName         = "org.gnome.Shell"
	ExtensionsName    = "org.gnome.Shell.Extensions"
	ScreenSaverName   = "org.gnome.ScreenSaver"
	NotificationsName = "org.freedesktop.Notifications"
)

// Shell plays GNOME Shell on a bus.
type Shell struct {
	Conn *dbus.Conn

	mu        sync.Mutex
	calls     []string
	enableOK  bool
	disableOK bool
	locked    bool
	notices   []string
	gate      *ActiveGate // GetActive waits on it (nil: answers at once)
}

// Options say how the shell answers.
type Options struct {
	Version   string // ShellVersion ("": the property is missing)
	EnableOK  bool   // what EnableExtension answers
	DisableOK bool   // what DisableExtension answers
	Locked    bool   // what GetActive answers at first
	// Names it owns (nil: all four)
	Names []string
}

// Start connects a shell to the bus at addr; it leaves the bus when the
// test ends.
func Start(t testing.TB, addr string, o Options) *Shell {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &Shell{Conn: conn, enableOK: o.EnableOK, disableOK: o.DisableOK, locked: o.Locked}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if o.Version != "" {
		_, err := prop.Export(conn, "/org/gnome/Shell", prop.Map{ShellName: {
			"ShellVersion": {Value: o.Version, Writable: false, Emit: prop.EmitFalse},
		}})
		must(err)
	}
	must(conn.Export(extensions{s}, "/org/gnome/Shell/Extensions", ExtensionsName))
	must(conn.Export(screenSaver{s}, "/org/gnome/ScreenSaver", ScreenSaverName))
	must(conn.Export(notifications{s}, "/org/freedesktop/Notifications", NotificationsName))
	names := o.Names
	if names == nil {
		names = []string{ShellName, ExtensionsName, ScreenSaverName, NotificationsName}
	}
	for _, n := range names {
		reply, err := conn.RequestName(n, dbus.NameFlagDoNotQueue)
		must(err)
		if reply != dbus.RequestNameReplyPrimaryOwner {
			t.Fatalf("%s is owned already", n)
		}
	}
	return s
}

// Calls are the extension calls the shell got ("EnableExtension uuid").
func (s *Shell) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Notices are the notifications shown ("app|summary|body").
func (s *Shell) Notices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.notices...)
}

// SetLocked changes GetActive's answer and emits ActiveChanged.
func (s *Shell) SetLocked(t testing.TB, locked bool) {
	t.Helper()
	s.mu.Lock()
	s.locked = locked
	s.mu.Unlock()
	if err := s.Conn.Emit("/org/gnome/ScreenSaver", ScreenSaverName+".ActiveChanged", locked); err != nil {
		t.Fatal(err)
	}
}

// SetLockedQuietly changes GetActive's answer without ActiveChanged (a
// screen saver whose signal never comes).
func (s *Shell) SetLockedQuietly(locked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locked = locked
}

// Leave gives up org.gnome.Shell (the shell went away; the connection
// stays).
func (s *Shell) Leave(t testing.TB) { s.Release(t, ShellName) }

// Release gives up one of the shell's names (the connection stays).
func (s *Shell) Release(t testing.TB, name string) {
	t.Helper()
	if _, err := s.Conn.ReleaseName(name); err != nil {
		t.Fatal(err)
	}
}

// ActiveGate holds GetActive's answers back: each call takes the answer of
// the moment it came and waits until Open.
type ActiveGate struct {
	mu      sync.Mutex
	pending int
	open    chan struct{}
	once    sync.Once
}

// HoldGetActive makes GetActive wait until the gate it returns is opened
// (at the latest when the test ends).
func (s *Shell) HoldGetActive(t testing.TB) *ActiveGate {
	g := &ActiveGate{open: make(chan struct{})}
	t.Cleanup(g.Open)
	s.mu.Lock()
	s.gate = g
	s.mu.Unlock()
	return g
}

// Pending is the number of GetActive calls waiting.
func (g *ActiveGate) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending
}

// Open lets every waiting GetActive answer (and later ones answer at once).
func (g *ActiveGate) Open() { g.once.Do(func() { close(g.open) }) }

type extensions struct{ s *Shell }

func (e extensions) EnableExtension(uuid string) (bool, *dbus.Error) {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	e.s.calls = append(e.s.calls, "EnableExtension "+uuid)
	return e.s.enableOK, nil
}

func (e extensions) DisableExtension(uuid string) (bool, *dbus.Error) {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	e.s.calls = append(e.s.calls, "DisableExtension "+uuid)
	return e.s.disableOK, nil
}

type screenSaver struct{ s *Shell }

func (x screenSaver) GetActive() (bool, *dbus.Error) {
	x.s.mu.Lock()
	locked, g := x.s.locked, x.s.gate
	x.s.mu.Unlock()
	if g != nil {
		g.mu.Lock()
		g.pending++
		g.mu.Unlock()
		<-g.open
	}
	return locked, nil
}

type notifications struct{ s *Shell }

func (n notifications) Notify(app string, replaces uint32, icon, summary, body string, actions []string,
	hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	n.s.notices = append(n.s.notices, fmt.Sprintf("%s|%s|%s", app, summary, body))
	return uint32(len(n.s.notices)), nil
}
