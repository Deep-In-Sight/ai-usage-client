package cli

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
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
