package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/gnomeext"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
)

// gnomeEnv is a desktop install's environment on a GNOME session whose
// session bus is at addr ("": none given).
func gnomeEnv(t *testing.T, addr string) *testEnv {
	t.Helper()
	te := newEnv(t)
	te.vars["XDG_SESSION_TYPE"] = "wayland"
	te.vars["XDG_CURRENT_DESKTOP"] = "ubuntu:GNOME"
	if addr != "" {
		te.vars["DBUS_SESSION_BUS_ADDRESS"] = addr
	}
	te.runner.reply = systemdOK("yes", nil)
	return te
}

// panelDir is where GNOME Shell looks for the panel extension of te's user.
func (te *testEnv) panelDir() string {
	return filepath.Join(te.home, ".local", "share", "gnome-shell", "extensions", gnomeext.UUID)
}

func isCurrent(t *testing.T, dir, form string) bool {
	t.Helper()
	ok, err := gnomeext.Current(dir, form)
	must(t, err)
	return ok
}

func mtimes(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	m := map[string]time.Time{}
	must(t, filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		m[p] = fi.ModTime()
		return nil
	}))
	return m
}

func absent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("%s is there (%v)", p, err)
	}
}

// Design 6.6: a desktop install on a session whose GNOME Shell answers
// writes the extension for that shell and says once that it appears after
// the next login; a second install finds it current and leaves it alone,
// without the line.
func TestInstallWritesExtensionWhenShellAnswers(t *testing.T) {
	bus := fakeshell.NewBus(t, "")
	sh := fakeshell.Start(t, bus.Addr, fakeshell.Options{Version: "46.1"})
	te := gnomeEnv(t, bus.Addr)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	if !isCurrent(t, te.panelDir(), "gnome46") {
		t.Fatal("not the GNOME 46/50 form")
	}
	eq(t, strings.Count(te.out.String(), text.PanelAfterRelogin), 1)
	before := mtimes(t, te.panelDir())
	time.Sleep(20 * time.Millisecond)
	te.out.Reset()
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	eq(t, strings.Count(te.out.String(), text.PanelAfterRelogin), 0)
	eq(t, mtimes(t, te.panelDir()), before)
	// install never turns the extension on (the bridge does, once)
	eq(t, len(sh.Calls()), 0)
}

// RF3: no session bus (SSH, "su -", before the first GNOME login): the
// install succeeds, writes no extension, says nothing of the panel and
// never starts a bus (dbus-launch), also for an "autolaunch:" address.
func TestInstallWithoutSessionBus(t *testing.T) {
	rt, ran := fakeshell.NoBus(t)
	for _, addr := range []string{"", "autolaunch:"} {
		te := gnomeEnv(t, addr)
		te.vars["XDG_RUNTIME_DIR"] = rt
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
		eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
		absent(t, filepath.Join(te.home, ".local", "share", "gnome-shell"))
		if strings.Contains(te.out.String(), text.PanelAfterRelogin) {
			t.Fatalf("%q: %s", addr, te.out.String())
		}
	}
	if r := ran("dbus-launch"); r != "" {
		t.Fatalf("dbus-launch ran: %s", r)
	}
}

// --headless never writes the extension, even with a shell on the bus.
func TestInstallHeadlessSkipsExtension(t *testing.T) {
	bus := fakeshell.NewBus(t, "")
	fakeshell.Start(t, bus.Addr, fakeshell.Options{Version: "46.0"})
	te := gnomeEnv(t, bus.Addr)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	absent(t, filepath.Join(te.home, ".local", "share", "gnome-shell"))
	if strings.Contains(te.out.String(), text.PanelAfterRelogin) {
		t.Fatal(te.out.String())
	}
}

// Another desktop (a session bus without org.gnome.Shell), or a GNOME
// Shell the panel does not support: nothing is written, no line, and the
// install does not wait long.
func TestInstallOnAnotherDesktopWritesNothing(t *testing.T) {
	for _, shell := range []*fakeshell.Options{nil, {Version: "44.2"}, {Version: ""}} {
		bus := fakeshell.NewBus(t, "")
		if shell != nil {
			fakeshell.Start(t, bus.Addr, *shell)
		}
		te := gnomeEnv(t, bus.Addr)
		start := time.Now()
		eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("%v: install took %v", shell, d)
		}
		absent(t, filepath.Join(te.home, ".local", "share", "gnome-shell"))
		if strings.Contains(te.out.String(), text.PanelAfterRelogin) {
			t.Fatalf("%v: %s", shell, te.out.String())
		}
	}
}

// An update writes the extension again when the copy on disk is stale (a
// changed file, an extra file) or for another shell (an Ubuntu upgrade
// from 42 to 46), without the line: the folder was there already.
func TestUpgradeRewritesAStaleExtension(t *testing.T) {
	bus := fakeshell.NewBus(t, "")
	fakeshell.Start(t, bus.Addr, fakeshell.Options{Version: "42.9"})
	te := gnomeEnv(t, bus.Addr)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	if !isCurrent(t, te.panelDir(), "gnome42") {
		t.Fatal("not the GNOME 42 form")
	}
	must(t, os.WriteFile(filepath.Join(te.panelDir(), "view.js"), []byte("// old"), 0o644))
	must(t, os.WriteFile(filepath.Join(te.panelDir(), "old.js"), []byte("// old"), 0o644))
	te.out.Reset()
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	if !isCurrent(t, te.panelDir(), "gnome42") {
		t.Fatal("the stale copy was not written again")
	}
	eq(t, strings.Count(te.out.String(), text.PanelAfterRelogin), 0)

	// the same user after an upgrade to GNOME 46
	bus2 := fakeshell.NewBus(t, "")
	fakeshell.Start(t, bus2.Addr, fakeshell.Options{Version: "46.0"})
	te.vars["DBUS_SESSION_BUS_ADDRESS"] = bus2.Addr
	te.out.Reset()
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	if !isCurrent(t, te.panelDir(), "gnome46") {
		t.Fatal("not the GNOME 46/50 form after the upgrade")
	}
	eq(t, strings.Count(te.out.String(), text.PanelAfterRelogin), 0)
}

// Remove (design 6.6): the shell turns the extension off while its folder
// is still there (so it calls the extension's disable()), then the folder
// and any staging leftover go. A Disable that answers an error (GNOME 46
// once said NoReply although the extension was off: stage 0) does not
// stop the removal.
func TestRemoveDisablesThenDeletes(t *testing.T) {
	for _, fails := range []bool{false, true} {
		var mu sync.Mutex
		var folderAtCall []bool
		bus := fakeshell.NewBus(t, "")
		te := gnomeEnv(t, bus.Addr)
		meta := filepath.Join(te.panelDir(), "metadata.json")
		sh := fakeshell.Start(t, bus.Addr, fakeshell.Options{Version: "46.0", DisableOK: true, DisableFails: fails,
			OnCall: func(string) {
				_, err := os.Stat(meta)
				mu.Lock()
				folderAtCall = append(folderAtCall, err == nil)
				mu.Unlock()
			}})
		eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
		staging := filepath.Join(te.home, ".local", "share", "gnome-shell", ".ai-usage-panel.new-4242")
		must(t, os.MkdirAll(staging, 0o755))
		te.err.Reset()
		eq(t, te.main("uninstall"), 0)
		eq(t, sh.Calls(), []string{"DisableExtension " + gnomeext.UUID})
		mu.Lock()
		eq(t, folderAtCall, []bool{true})
		mu.Unlock()
		absent(t, te.panelDir())
		absent(t, staging)
		if got := strings.Count(te.err.String(), "turning the panel extension off"); got != map[bool]int{false: 0, true: 1}[fails] {
			t.Fatalf("fails=%v: stderr %q", fails, te.err.String())
		}
	}
}

// Remove without a session bus deletes the folder anyway (the uuid left in
// the shell's lists is ignored by the shell) and starts no bus.
func TestRemoveWithoutBusDeletesTheFolder(t *testing.T) {
	rt, ran := fakeshell.NoBus(t)
	te := gnomeEnv(t, "")
	te.vars["XDG_RUNTIME_DIR"] = rt
	dir := te.panelDir()
	_, err := gnomeext.Write(dir, 46)
	must(t, err)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	eq(t, te.main("uninstall"), 0)
	absent(t, dir)
	if r := ran("dbus-launch"); r != "" {
		t.Fatalf("dbus-launch ran: %s", r)
	}
	// nothing else of the user's extensions folder is touched
	other := filepath.Join(filepath.Dir(dir), "someone-else@example.com")
	must(t, os.MkdirAll(other, 0o755))
	eq(t, te.main("uninstall"), 0)
	if _, err := os.Stat(other); err != nil {
		t.Fatal("another extension was removed")
	}
	if !slices.Contains(removalSteps("linux", false), "remove-gnome-extension") {
		t.Fatal("no step")
	}
}
