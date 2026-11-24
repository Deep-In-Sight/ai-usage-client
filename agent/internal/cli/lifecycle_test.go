package cli

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// The agent that install starts on Linux has no standard error anyone
// reads (it goes to /dev/null): run keeps agent.log in the data folder,
// with the tray's own messages too, and with no secret or home path in it.
func TestLinuxRunWritesAgentLog(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux"
	te.vars["XDG_SESSION_TYPE"] = "wayland"
	te.helpers.onReady = func(*fakeHelper) {
		// anything this process logs through the standard logger
		log.Printf("tray: key test-ingest-key-0123456789 under %s", te.home)
	}
	te.helpers.autoClick = "quit"
	eq(t, te.main("run"), 0)
	got := read(t, te.Dirs.File(paths.LogFile))
	for _, want := range []string{"ai-usage 0.0.0-dev started", "tray: key [ingest key] under ~", "ai-usage stopped"} {
		contains(t, got, want)
	}
	for _, bad := range []string{"test-ingest-key-0123456789", te.home} {
		if strings.Contains(got, bad) {
			t.Fatalf("%q in agent.log:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, " ai-usage stopped\n") || strings.Count(got, "\n") < 2 {
		t.Fatalf("%q", got)
	}
	// standard error keeps the lines as well (the journal reads it)
	contains(t, te.err.String(), "ai-usage stopped")
}

// launchd (macOS) already writes standard error to agent.log: the lines
// are not written there twice.
func TestLaunchdLogIsNotWrittenTwice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("standard error as a file in the data folder: Unix only")
	}
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "darwin"
	te.helpers.autoClick = "quit"
	p := te.Dirs.File(paths.LogFile)
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	defer f.Close()
	te.Stderr = f
	eq(t, te.main("run"), 0)
	got := read(t, p)
	eq(t, strings.Count(got, "ai-usage stopped"), 1)
}

func TestStatusNamesTheLogFile(t *testing.T) {
	te := newEnv(t)
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated", Providers: claudeSnap(status.ProviderStatus{State: "ok"})}))
	eq(t, te.main("status"), 0)
	want := "Log:        " + te.Dirs.File(paths.LogFile)
	if runtime.GOOS != "windows" {
		rel, err := filepath.Rel(te.home, te.Dirs.File(paths.LogFile))
		must(t, err)
		want = "Log:        ~/" + filepath.ToSlash(rel)
	}
	contains(t, te.out.String(), want+"\n")
}

// A tray host that comes late (the extension loads after login) is noticed
// when it appears: the helper waits for it by signal and says ready, and
// the server hears "tray" at once (no 5-minute recheck).
func TestLateTrayHostIsNoticedWhenItAppears(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.helpers.why = "no StatusNotifierWatcher"
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "headless" })
	at := time.Now()
	h.say(trayproto.Msg{Type: trayproto.TypeReady})
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "tray" })
	if d := time.Since(at); d > 2*time.Second {
		t.Fatalf("the server heard tray %v after the icon came up", d)
	}
	cancel()
	eq(t, waitCode(t, code), 0)
}

// The helper's connection to the session bus is lost (GNOME restarts the
// bus at logout): the helper ends with 75 and the agent starts it again
// within about a second; the main process keeps running, keeps run.lock
// and keeps syncing, telling the server there is no icon meanwhile
// (design D14; v0.8.1 restarted the whole process instead).
func TestSessionLossRestartsTheHelper(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "tray" })
	te.helpers.mu.Lock()
	te.helpers.why = "no StatusNotifierWatcher" // the new bus has no tray host yet
	te.helpers.mu.Unlock()
	lostAt := time.Now()
	h.exit(trayproto.ExitSessionLost)
	h2 := te.helpers.ready()
	if d := time.Since(lostAt); d < 900*time.Millisecond || d > 5*time.Second {
		t.Fatalf("helper started again %v after the loss, want about 1 s", d)
	}
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "headless" })
	n := len(fs.requests())
	h2.say(trayproto.Msg{Type: trayproto.TypeReady}) // the host appeared on the new bus
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > n && modeOf(r[len(r)-1]) == "tray" })
	if _, err := procs.Lock(te.Dirs.File(paths.LockFile)); err == nil {
		t.Fatal("run.lock was let go")
	}
	cancel()
	eq(t, waitCode(t, code), 0)
	logText := read(t, te.Dirs.File(paths.LogFile))
	contains(t, logText, "tray helper none ended (exit 75); starting it again in 1s")
	if strings.Contains(logText, "ai-usage restarting") || strings.Count(logText, " started (pid ") != 1 {
		t.Fatalf("the main process restarted:\n%s", logText)
	}
}

// A run that stops (SIGTERM) while a helper waits to be started again
// starts no helper and ends.
func TestStopWhileAHelperWaitsToRestart(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	h.exit(3)
	time.Sleep(100 * time.Millisecond)
	cancel()
	eq(t, waitCode(t, code), 0)
	time.Sleep(1500 * time.Millisecond)
	eq(t, te.helpers.count(), 1)
}

// When its helper ends the agent itself goes on: killing the main process
// is what ends a helper (its standard input closes), never the reverse.
func TestHelperCrashLoopNeverStopsSyncing(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	cancel, code := te.mainAsync("run")
	for range 3 {
		h := te.helpers.ready()
		h.exit(2)
	}
	n := len(fs.requests())
	h := te.helpers.ready()
	h.click("refresh")
	fs.waitFor(t, 10*time.Second, func(r []protocol.Request) bool { return len(r) > n })
	cancel()
	eq(t, waitCode(t, code), 0)
}

// Newest instance wins: a desktop run takes the lock over (the real
// mechanism is procs.TakeOver); a headless run never does.
func TestDesktopRunTakesOver(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process identities (and so takeover) are Linux and macOS only")
	}
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux"
	te.vars["XDG_SESSION_TYPE"] = "x11"
	var called int
	te.TakeOver = func(ctx context.Context, path string, self procs.Identity) (func(), procs.Identity, error) {
		called++
		eq(t, path, te.Dirs.File(paths.LockFile))
		unlock, err := procs.LockAs(path, self)
		return unlock, procs.Identity{PID: 4242, Start: 1}, err
	}
	te.helpers.autoClick = "quit"
	eq(t, te.main("run"), 0)
	eq(t, called, 1)
	contains(t, read(t, te.Dirs.File(paths.LogFile)), "took over from the agent that ran before (pid 4242)")

	called = 0
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	te.Context = stopped // a headless run ends with its context
	eq(t, te.main("run", "--headless"), 0)
	eq(t, called, 0)
	te.Context = nil
	te.GOOS = "windows"
	eq(t, te.main("run"), 0)
	eq(t, called, 0)
}

func TestRunYieldsToANewerAgent(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process identities (and so takeover) are Linux and macOS only")
	}
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux"
	te.vars["XDG_SESSION_TYPE"] = "x11"
	te.TakeOver = func(context.Context, string, procs.Identity) (func(), procs.Identity, error) {
		return nil, procs.Identity{PID: 4343, Start: 9}, procs.ErrNewer
	}
	eq(t, te.main("run"), 1)
	eq(t, te.helpers.count(), 0) // no tray shown
	contains(t, te.err.String(), "ai-usage: already running (pid 4343, started after this one)")
	eq(t, len(fs.requests()), 0)
}

// A newcomer that could not run (a broken config.json, an unreadable
// backlog or device id) leaves the running agent alone: it finds out
// before it asks the holder of run.lock to stop (review m2).
func TestNewcomerThatCannotRunLeavesTheRunningAgent(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process identities (and so takeover) are Linux and macOS only")
	}
	for name, spoil := range map[string]func(te *testEnv){
		"config":    func(te *testEnv) { must(t, os.WriteFile(te.Dirs.File(paths.ConfigFile), []byte("{not json"), 0o600)) },
		"backlog":   func(te *testEnv) { must(t, os.MkdirAll(te.Dirs.File(paths.BacklogFile), 0o700)) },
		"device id": func(te *testEnv) { must(t, os.MkdirAll(te.Dirs.File(paths.DeviceIDFile), 0o700)) },
	} {
		t.Run(name, func(t *testing.T) {
			fs := newFakeServer(t)
			te := trayEnv(t, fs)
			te.GOOS = "linux"
			te.vars["XDG_SESSION_TYPE"] = "x11"
			spoil(te)
			called := 0
			te.TakeOver = func(ctx context.Context, path string, self procs.Identity) (func(), procs.Identity, error) {
				called++
				unlock, err := procs.LockAs(path, self)
				return unlock, procs.Identity{PID: 4242, Start: 1}, err
			}
			eq(t, te.main("run"), 1)
			eq(t, called, 0)
			eq(t, len(fs.requests()), 0)
		})
	}
}

// A crash report the Go runtime wrote into agent.log (past the scrubber)
// is scrubbed by the next run, in agent.log and agent.log.1 (review m3).
func TestRunScrubsWhatACrashLeft(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux"
	te.vars["XDG_SESSION_TYPE"] = "wayland"
	te.helpers.autoClick = "quit"
	p := te.Dirs.File(paths.LogFile)
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	crash := "panic: key test-ingest-key-0123456789 under " + te.home + "\n\ngoroutine 1 [running]:\n"
	must(t, os.WriteFile(p, []byte(crash), 0o600))
	must(t, os.WriteFile(p+".1", []byte(crash), 0o600))
	eq(t, te.main("run"), 0)
	for _, f := range []string{p, p + ".1"} {
		got := read(t, f)
		contains(t, got, "panic: key [ingest key] under ~\n")
		for _, bad := range []string{"test-ingest-key-0123456789", te.home} {
			if strings.Contains(got, bad) {
				t.Fatalf("%q in %s:\n%s", bad, f, got)
			}
		}
	}
}
