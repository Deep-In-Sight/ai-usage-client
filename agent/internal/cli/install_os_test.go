package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/version"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/winapp"
)

// recorder is one ordered list of what the fakes were asked to do.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// fakeMethod is an autostart method and Toggler that records its calls; Stop
// and Register also note what the installed executable held at that moment.
type fakeMethod struct {
	name        string
	rec         *recorder
	exe         string // the install location, read at Stop and Register
	registerErr error
	enabled     bool
}

func (f *fakeMethod) content() string {
	b, err := os.ReadFile(f.exe)
	if err != nil {
		return "none"
	}
	return string(b)
}

func (f *fakeMethod) Name() string                   { return f.name }
func (f *fakeMethod) Available(context.Context) bool { return true }
func (f *fakeMethod) Register(_ context.Context, exe string) error {
	f.rec.add(f.name + ".register:" + f.content())
	f.enabled = true
	return f.registerErr
}
func (f *fakeMethod) Stop(context.Context) error {
	f.rec.add(f.name + ".stop:" + f.content())
	return nil
}
func (f *fakeMethod) Unregister(context.Context) error {
	f.rec.add(f.name + ".unregister")
	f.enabled = false
	return nil
}
func (f *fakeMethod) State(context.Context) string { return "registered" }
func (f *fakeMethod) Enabled(context.Context) bool { return f.enabled }
func (f *fakeMethod) SetEnabled(_ context.Context, _ string, on bool) error {
	f.enabled = on
	return nil
}

type fakeWinApp struct {
	rec     *recorder
	entries []winapp.Entry
}

func (f *fakeWinApp) Register(e winapp.Entry) error {
	f.rec.add("winapp.register")
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeWinApp) Unregister() error {
	f.rec.add("winapp.unregister")
	return nil
}

// osEnv is a test environment that acts like goos, with fake autostart
// methods: the chosen one ("main") and, on Linux, the other one ("other").
func osEnv(t *testing.T, goos string) (*testEnv, *recorder, *fakeMethod, *fakeMethod, *fakeWinApp) {
	t.Helper()
	te := newEnv(t)
	te.GOOS = goos
	rec := &recorder{}
	main := &fakeMethod{name: "main", rec: rec, exe: te.Dirs.InstallExe}
	other := &fakeMethod{name: "other", rec: rec, exe: te.Dirs.InstallExe}
	wa := &fakeWinApp{rec: rec}
	te.Autostart = func(bool) (autostart.Method, error) { return main, nil }
	te.Others = func(bool) []autostart.Method {
		if goos == "linux" {
			return []autostart.Method{other}
		}
		return nil
	}
	te.WinApp = wa
	if goos == "linux" {
		te.vars["XDG_SESSION_TYPE"] = "wayland"
		te.vars["XDG_CURRENT_DESKTOP"] = "ubuntu:GNOME"
	}
	inner := te.StartDetached
	te.StartDetached = func(exe string, args ...string) error {
		rec.add("detached:" + strings.Join(args, " "))
		return inner(exe, args...)
	}
	return te, rec, main, other, wa
}

func TestInstallWindowsWritesEntryAndNotice(t *testing.T) {
	te, rec, _, _, wa := osEnv(t, "windows")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	eq(t, len(wa.entries), 1)
	e := wa.entries[0]
	eq(t, e.DisplayVersion, version.Version)
	eq(t, e.UninstallString, `"`+te.Dirs.InstallExe+`" uninstall`)
	eq(t, e.EstimatedSizeKB, uint32(1)) // "BIN"
	eq(t, read(t, te.Dirs.File(paths.NotifyFile)), version.Version)
	contains(t, te.out.String(), "The Claude Usage icon is under the ^ arrow on the taskbar. Drag it onto the taskbar to keep it in view.")
	eq(t, rec.list(), []string{"main.stop:none", "winapp.register", "main.register:BIN"})
	eq(t, len(te.detached), 0)
}

func TestInstallStartFailedFallsBackToDetached(t *testing.T) {
	te, rec, main, _, _ := osEnv(t, "darwin")
	main.registerErr = autostart.ErrStartFailed
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	eq(t, te.detached, [][]string{{te.Dirs.InstallExe, "run"}})
	eq(t, rec.list()[len(rec.list())-1], "detached:run")
	contains(t, te.out.String(), "Look for the Claude Usage ring in the menu bar.")

	te, _, main, _, _ = osEnv(t, "windows")
	main.registerErr = errors.New("registry: access denied")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 1)
	contains(t, te.err.String(), "access denied")
}

func TestInstallLinuxDesktopRemovesSystemdUnit(t *testing.T) {
	te, rec, _, _, wa := osEnv(t, "linux")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	eq(t, rec.list(), []string{"main.stop:none", "other.stop:none", "main.register:BIN", "other.unregister"})
	eq(t, len(wa.entries), 0)
	contains(t, te.out.String(), "Look for the Claude Usage ring in the top bar.")
	for _, c := range te.runner.calls {
		if c[0] == "loginctl" {
			t.Fatalf("linger touched on a desktop: %v", c)
		}
	}
}

func TestInstallUpgradeStopsTrayKeepsDeviceID(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		te, rec, _, _, _ := osEnv(t, goos)
		cfgPath := writeCfg(t, validConfig)
		eq(t, te.main("install", "--config", cfgPath), 0)
		id, err := config.DeviceID(te.Dirs.File(paths.DeviceIDFile))
		must(t, err)
		must(t, os.WriteFile(te.Dirs.File(paths.BacklogFile), []byte(`[{"account_uuid":"acc-1","fetched_at":"2026-01-05T08:00:00.000Z","http_status":200,"usage":null}]`), 0o600))
		must(t, os.Remove(te.Dirs.File(paths.NotifyFile))) // the first run showed the notice

		must(t, os.WriteFile(te.Exe, []byte("BIN2"), 0o755))
		rec.calls = nil
		eq(t, te.main("install", "--config", cfgPath), 0)
		calls := rec.list()
		stop := slices.Index(calls, "main.stop:BIN") // stopped while the old binary was in place
		reg := slices.Index(calls, "main.register:BIN2")
		if stop < 0 || reg < stop {
			t.Fatalf("%s: %v", goos, calls)
		}
		eq(t, read(t, te.Dirs.InstallExe), "BIN2")
		id2, _ := config.DeviceID(te.Dirs.File(paths.DeviceIDFile))
		eq(t, id2, id)
		contains(t, read(t, te.Dirs.File(paths.BacklogFile)), "acc-1")
		eq(t, read(t, te.Dirs.File(paths.NotifyFile)), version.Version)
	}
}

func TestInstallWarnsAboutGnomeExtension(t *testing.T) {
	te, _, _, _, _ := osEnv(t, "linux")
	must(t, os.MkdirAll(filepath.Join(te.home, ".local", "share", "gnome-shell", "extensions", "claude-usage@localhost"), 0o755))
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	contains(t, te.out.String(), "Note: the Claude Usage GNOME extension is also installed and makes its own requests. Turn it off with: gnome-extensions disable claude-usage@localhost")

	te, _, _, _, _ = osEnv(t, "linux")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	if strings.Contains(te.out.String(), "GNOME extension") {
		t.Fatal("warned without the extension")
	}
}

func TestInstallAlertsWithoutConsole(t *testing.T) {
	te, _, _, _, _ := osEnv(t, "windows")
	var alerts []string
	te.HasConsole = func() bool { return false }
	te.Alert = func(title, text string) { alerts = append(alerts, title+"|"+text) }
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	eq(t, len(alerts), 1)
	contains(t, alerts[0], "Claude Usage|Claude Usage is installed and running.")

	alerts = nil
	eq(t, te.main("install", "--config", writeCfg(t, `{"server_urls":[]}`)), 1)
	eq(t, len(alerts), 1)
	contains(t, alerts[0], "Claude Usage|Claude Usage could not be installed:\n")
}

func TestNoArgsOnInstalledExeRunsOnWindows(t *testing.T) {
	te, _, _, _, _ := osEnv(t, "windows")
	te.Exe = te.Dirs.InstallExe
	must(t, os.MkdirAll(filepath.Dir(te.Exe), 0o755))
	must(t, os.WriteFile(te.Exe, []byte("BIN"), 0o755))
	unlock, err := procs.Lock(te.Dirs.File(paths.LockFile))
	must(t, err)
	eq(t, te.main(), 1)
	contains(t, te.err.String(), "already running") // it went the run way
	unlock()

	te.GOOS = "linux"
	te.err.Reset()
	eq(t, te.main(), 2)
	contains(t, te.err.String(), "usage")
}

func TestRemovalSteps(t *testing.T) {
	cases := []struct {
		goos      string
		inProcess bool
		want      []string
	}{
		{"windows", false, []string{"stop", "kill", "unregister-autostart", "unregister-app", "remove-exe", "remove-install-dir", "remove-data"}},
		{"windows", true, []string{"stop-engine", "unlock", "unregister-autostart", "unregister-app", "remove-exe", "remove-install-dir", "remove-data"}},
		{"darwin", false, []string{"stop", "kill", "unregister-autostart", "remove-data"}},
		{"darwin", true, []string{"stop-engine", "unlock", "remove-plist", "remove-data", "bootout-self"}},
		{"linux", false, []string{"stop", "kill", "unregister-autostart", "remove-exe", "remove-data"}},
		{"linux", true, []string{"stop-engine", "unlock", "unregister-autostart", "remove-exe", "remove-data"}},
	}
	for _, c := range cases {
		eq(t, removalSteps(c.goos, c.inProcess), c.want)
	}
}

func TestInProcessRemoveReleasesLockFirst(t *testing.T) {
	te, rec, _, _, _ := osEnv(t, "windows")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	unlock, err := procs.Lock(te.Dirs.File(paths.LockFile))
	must(t, err)
	rec.calls = nil
	r := newRemover(te.Env)
	r.stopEngine = func() { rec.add("stop-engine") }
	r.unlock = func() { rec.add("unlock"); unlock() }
	must(t, r.run(removalSteps("windows", true)))
	calls := rec.list()
	eq(t, calls[:2], []string{"stop-engine", "unlock"})
	if !slices.Contains(calls, "main.unregister") || !slices.Contains(calls, "winapp.unregister") {
		t.Fatalf("%v", calls)
	}
	if _, err := os.Stat(te.Dirs.Data); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("data folder left")
	}
	if _, err := os.Stat(te.Dirs.InstallExe); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("executable left")
	}
	again, err := procs.Lock(te.Dirs.File(paths.LockFile)) // (this makes the folder again)
	must(t, err)
	again()
}

func TestUninstallRemovesEntryAndDirs(t *testing.T) {
	te, rec, _, _, _ := osEnv(t, "windows")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	rec.calls = nil
	te.out.Reset()
	eq(t, te.main("uninstall"), 0)
	contains(t, te.out.String(), "removed")
	calls := rec.list()
	for _, want := range []string{"main.stop:BIN", "main.unregister", "winapp.unregister"} {
		if !slices.Contains(calls, want) {
			t.Fatalf("%s missing in %v", want, calls)
		}
	}
	for _, p := range []string{te.Dirs.InstallExe, filepath.Dir(te.Dirs.InstallExe), te.Dirs.Data} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left", p)
		}
	}

	// without a console the result is shown in a message box
	var alerts []string
	te.HasConsole = func() bool { return false }
	te.Alert = func(title, text string) { alerts = append(alerts, text) }
	eq(t, te.main("uninstall"), 0)
	eq(t, alerts, []string{"Claude Usage has been removed."})
}

func TestUninstallRemovesInstalledExeItselfOnWindows(t *testing.T) {
	te, _, _, _, _ := osEnv(t, "windows")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	te.Exe = te.Dirs.InstallExe // run from "Installed apps"
	tmp := t.TempDir()
	te.vars["TEMP"] = tmp
	eq(t, te.main("uninstall"), 0)
	if _, err := os.Stat(te.Dirs.InstallExe); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("executable left in place")
	}
	moved, _ := filepath.Glob(filepath.Join(tmp, "claude-usage-removed-*.exe"))
	eq(t, len(moved), 1)
}

func TestStatusNamesTheMethodInUse(t *testing.T) {
	te, _, main, other, _ := osEnv(t, "linux")
	main.name, other.name = "systemd-user", "xdg-autostart"
	te.Autostart = func(bool) (autostart.Method, error) { return &stateMethod{main, "not installed"}, nil }
	te.Others = func(bool) []autostart.Method { return []autostart.Method{&stateMethod{other, "registered"}} }
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated", AuthState: "ok"}))
	eq(t, te.main("status"), 0)
	contains(t, te.out.String(), "Service:    registered (xdg-autostart)")
}

// stateMethod reports a fixed state.
type stateMethod struct {
	*fakeMethod
	state string
}

func (s *stateMethod) State(context.Context) string { return s.state }

func TestReplaceRetriesOnWindows(t *testing.T) {
	calls := 0
	failTwice := func(string, string) error {
		calls++
		if calls <= 2 {
			return errors.New("Access is denied.")
		}
		return nil
	}
	must(t, replaceFile("windows", "a", "b", failTwice, 0))
	eq(t, calls, 3)
	calls = 0
	if err := replaceFile("linux", "a", "b", failTwice, 0); err == nil {
		t.Fatal("linux retried")
	}
	eq(t, calls, 1)
	calls = -100 // always fails on Windows: gives up
	if err := replaceFile("windows", "a", "b", func(string, string) error { calls++; return errors.New("denied") }, 0); err == nil {
		t.Fatal("no error")
	}
}
