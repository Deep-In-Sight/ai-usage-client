package cli

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
)

// The agent that install starts on Linux has no standard error anyone
// reads (it goes to /dev/null): run keeps agent.log in the data folder,
// with the tray's own messages too, and with no secret or home path in it.
func TestLinuxRunWritesAgentLog(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux"
	te.vars["XDG_SESSION_TYPE"] = "wayland"
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error {
		log.Printf("systray error: key test-ingest-key-0123456789 under %s", te.home)
		return nil
	}
	eq(t, te.main("run"), 0)
	got := read(t, te.Dirs.File(paths.LogFile))
	for _, want := range []string{"ai-usage 0.0.0-dev started", "systray error: key [ingest key] under ~", "ai-usage stopped"} {
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
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error { return nil }
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
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated", AuthState: "ok"}))
	eq(t, te.main("status"), 0)
	want := "Log:        " + te.Dirs.File(paths.LogFile)
	if runtime.GOOS != "windows" {
		rel, err := filepath.Rel(te.home, te.Dirs.File(paths.LogFile))
		must(t, err)
		want = "Log:        ~/" + filepath.ToSlash(rel)
	}
	contains(t, te.out.String(), want+"\n")
}

// A tray host that comes after the start-up wait (the extension loads late)
// is noticed when it appears, not at the next 5-minute recheck.
func TestLateTrayHostIsNoticedWhenItAppears(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	var hostUp atomic.Bool
	appear := make(chan struct{})
	te.TrayAvailable = func(context.Context) (bool, string) {
		if hostUp.Load() {
			return true, ""
		}
		return false, "no StatusNotifierWatcher"
	}
	var limits []time.Duration
	var mu sync.Mutex
	te.TrayWait = func(ctx context.Context, limit time.Duration) bool {
		mu.Lock()
		limits = append(limits, limit)
		mu.Unlock()
		select {
		case <-appear:
			return true
		case <-ctx.Done():
			return false
		}
	}
	shown := make(chan time.Time, 1)
	te.Tray = func(ctx context.Context, c tray.Controller, u <-chan struct{}, notice string) error {
		shown <- time.Now()
		<-ctx.Done()
		return nil
	}
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "headless" })
	hostUp.Store(true)
	at := time.Now()
	close(appear)
	select {
	case s := <-shown:
		if d := s.Sub(at); d > 2*time.Second {
			t.Fatalf("tray shown %v after the host appeared", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the tray was not shown when the host appeared")
	}
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "tray" })
	cancel()
	eq(t, waitCode(t, code), 0)
	mu.Lock()
	defer mu.Unlock()
	if len(limits) == 0 || limits[0] != trayRecheck {
		t.Fatalf("waits %v", limits)
	}
}

func TestRestartPlan(t *testing.T) {
	old := restartBase
	restartBase = 5 * time.Second
	t.Cleanup(func() { restartBase = old })
	cases := []struct {
		prev  int
		lived time.Duration
		wait  time.Duration
		next  int
	}{
		{0, 0, 5 * time.Second, 1},               // the first restart: after at least 5 s
		{0, time.Minute, 0, 1},                   // a run that lived long restarts at once
		{1, 2 * time.Second, 8 * time.Second, 2}, // 10 s for the second in a row
		{3, 0, 40 * time.Second, 4},              // doubling
		{20, 0, restartMax, 21},                  // capped
		{20, restartReset, 0, 1},                 // a long life starts the count again
		{9, restartReset - time.Second, restartMax - restartReset + time.Second, 10},
	}
	for _, c := range cases {
		wait, next := restartPlan(c.prev, c.lived)
		if wait != c.wait || next != c.next {
			t.Errorf("restartPlan(%d, %v) = %v, %d; want %v, %d", c.prev, c.lived, wait, next, c.wait, c.next)
		}
	}
	// however often it happens, restarts never come faster than one per
	// restartBase, and after a few at most one per restartMax
	lifetimes := time.Duration(0)
	prev := 0
	for i := 0; i < 20; i++ {
		wait, next := restartPlan(prev, 0)
		lifetimes += wait
		prev = next
	}
	if lifetimes < 2*time.Hour {
		t.Fatalf("20 quick restarts took only %v", lifetimes)
	}
}

func TestRestartsFromEnvironment(t *testing.T) {
	for v, want := range map[string]int{"": 0, "3": 3, "x": 0, "-2": 0} {
		eq(t, restartsFrom(func(string) string { return v }), want)
	}
}

// restartRecorder is a Restart that records instead of replacing the
// process.
type restartRecorder struct {
	mu    sync.Mutex
	args  []string
	n     int
	at    time.Time
	free  bool // run.lock was free when it was called
	calls int
}

func (r *restartRecorder) restart(te *testEnv) func([]string, int) error {
	return func(args []string, n int) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls++
		r.args, r.n, r.at = args, n, time.Now()
		if unlock, err := procs.Lock(te.Dirs.File(paths.LockFile)); err == nil {
			r.free = true
			unlock()
		}
		return nil
	}
}

// The tray's connection to the session bus is lost (GNOME restarts the bus
// at logout): the run reports no icon, keeps syncing for a while (so that
// restarts cannot come in a tight loop), then stops its engine, lets go of
// run.lock and replaces itself with a new run, which waits for a tray host
// on the new bus.
func TestSessionLossRestartsInPlace(t *testing.T) {
	old := restartBase
	restartBase = 600 * time.Millisecond
	t.Cleanup(func() { restartBase = old })
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	var lostAt time.Time
	te.Tray = func(ctx context.Context, c tray.Controller, u <-chan struct{}, notice string) error {
		fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
		lostAt = time.Now()
		return tray.ErrSessionLost
	}
	rec := &restartRecorder{}
	te.Restart = rec.restart(te)
	eq(t, te.main("run"), 0)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	eq(t, rec.calls, 1)
	eq(t, rec.args, []string{"run"})
	eq(t, rec.n, 1)
	if !rec.free {
		t.Fatal("run.lock still held when the run replaced itself")
	}
	if d := rec.at.Sub(lostAt); d < 500*time.Millisecond {
		t.Fatalf("restarted %v after the loss, want at least restartBase", d)
	}
	// the engine kept syncing in between, telling the server there is no icon
	var headless int
	for _, r := range fs.requests() {
		if modeOf(r) == "headless" {
			headless++
		}
	}
	if headless == 0 {
		t.Fatal("no headless sync between the loss and the restart")
	}
	logText := read(t, te.Dirs.File(paths.LogFile))
	contains(t, logText, "session bus")
	contains(t, logText, "ai-usage restarting")
}

func TestSessionLossRestartCountsUp(t *testing.T) {
	old := restartBase
	restartBase = 50 * time.Millisecond
	t.Cleanup(func() { restartBase = old })
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.vars["AI_USAGE_RESTARTS"] = "3"
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error { return tray.ErrSessionLost }
	rec := &restartRecorder{}
	te.Restart = rec.restart(te)
	start := time.Now()
	eq(t, te.main("run"), 0)
	eq(t, rec.n, 4)
	eq(t, rec.args, []string{"run"})
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("fourth restart in a row after %v, want at least 8 x restartBase", d)
	}
}

// A run that stops while it waits to restart (SIGTERM) does not restart.
func TestStopWhileWaitingToRestart(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	lost := make(chan struct{})
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error {
		close(lost)
		return tray.ErrSessionLost
	}
	rec := &restartRecorder{}
	te.Restart = rec.restart(te)
	cancel, code := te.mainAsync("run")
	<-lost
	time.Sleep(50 * time.Millisecond)
	cancel()
	eq(t, waitCode(t, code), 0)
	eq(t, rec.calls, 0)
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
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error { return nil }
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
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error {
		t.Error("tray shown")
		return nil
	}
	eq(t, te.main("run"), 1)
	contains(t, te.err.String(), "ai-usage: already running (pid 4343, started after this one)")
	eq(t, len(fs.requests()), 0)
}
