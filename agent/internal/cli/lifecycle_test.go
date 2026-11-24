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
