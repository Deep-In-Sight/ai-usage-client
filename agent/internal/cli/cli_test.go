package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/clock"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
)

const validConfig = `{"server_urls":["http://claude-usage.local","http://192.0.2.10:8700"],"ingest_key":"test-ingest-key-0123456789"}`

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%q not in:\n%s", sub, s)
	}
}

// call is one recorded runner call.
type runner struct {
	calls [][]string
	reply func(args []string) (string, error)
}

func (r *runner) run(_ context.Context, name string, args ...string) (string, error) {
	c := append([]string{name}, args...)
	r.calls = append(r.calls, c)
	if r.reply != nil {
		return r.reply(c)
	}
	return "", nil
}

type testEnv struct {
	Env
	home     string
	out, err *bytes.Buffer
	runner   *runner
	detached [][]string
	vars     map[string]string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	home := t.TempDir()
	vars := map[string]string{}
	if runtime.GOOS == "windows" {
		vars["LOCALAPPDATA"] = filepath.Join(home, "AppData", "Local")
	}
	getenv := func(k string) string { return vars[k] }
	dirs, err := paths.For(getenv, home)
	must(t, err)
	te := &testEnv{home: home, out: &bytes.Buffer{}, err: &bytes.Buffer{}, runner: &runner{}, vars: vars}
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "claude-usage-download")
	must(t, os.WriteFile(exe, []byte("BIN"), 0o755))
	te.Env = Env{
		Getenv: getenv, Stdout: te.out, Stderr: te.err, Exe: exe, Dirs: dirs, Run: te.runner.run,
		StartDetached: func(exe string, args ...string) error {
			te.detached = append(te.detached, append([]string{exe}, args...))
			return nil
		},
		Clock:     clock.NewFake(time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)),
		Autostart: func(headless bool) (autostart.Method, error) { return autostart.For(headless, home, te.runner.run) },
	}
	return te
}

func (te *testEnv) main(args ...string) int {
	te.Args = args
	return Main(te.Env)
}

func TestVersion(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("version"), 0)
	eq(t, te.out.String(), "claude-usage 0.0.0-dev "+runtime.GOOS+"/"+runtime.GOARCH+"\n")
}

func TestUnknownCommandAndNoArgsUsage(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("frobnicate"), 2)
	contains(t, te.err.String(), "usage")
	te = newEnv(t) // no config.json next to the executable
	eq(t, te.main(), 2)
	contains(t, te.err.String(), "usage")
}

func TestRunIgnoresClaudeConfigDirEnv(t *testing.T) {
	te := newEnv(t)
	te.vars["CLAUDE_CONFIG_DIR"] = "/elsewhere/claude"
	cfg, err := config.Parse([]byte(validConfig))
	must(t, err)
	o := readerOptions(cfg, te.Dirs)
	eq(t, o.ClaudeConfigDir, "")
	eq(t, o.Home, te.home)
	eq(t, o.KeychainService, "Claude Code-credentials")
	eq(t, o.UseKeychain, runtime.GOOS == "darwin")
	abs := filepath.Join(t.TempDir(), "claude")
	cfg.ClaudeConfigDir = abs
	eq(t, readerOptions(cfg, te.Dirs).ClaudeConfigDir, abs)
	cfg.KeychainService = "custom-item"
	eq(t, readerOptions(cfg, te.Dirs).KeychainService, "custom-item")
}

func TestSecondRunExitsWhenLocked(t *testing.T) {
	te := newEnv(t)
	must(t, config.Save(te.Dirs.File(paths.ConfigFile), mustParse(t, validConfig)))
	unlock, err := procs.Lock(te.Dirs.File(paths.LockFile))
	must(t, err)
	defer unlock()
	eq(t, te.main("run", "--headless"), 1)
	contains(t, te.err.String(), "already running")
}

func TestRunWithoutConfigFails(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("run"), 1)
	contains(t, te.err.String(), "config")
}

func mustParse(t *testing.T, s string) config.Config {
	t.Helper()
	c, err := config.Parse([]byte(s))
	must(t, err)
	return c
}

func TestStatusJSON(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("status"), 1) // no status file yet
	pct := 5.0
	snap := status.Snapshot{Version: "0.0.0-dev", PID: 1, Mode: "coordinated", Role: "poller", ServerURL: "http://claude-usage.local",
		LastSyncAt: time.Date(2026, 1, 5, 8, 59, 0, 0, time.UTC), LastSyncOK: true, AuthState: "ok",
		TokenExpiresAt: time.Date(2026, 1, 5, 17, 0, 0, 0, time.UTC), WeeklyPercent: &pct,
		WeeklyResetsAt: time.Date(2026, 1, 9, 6, 0, 0, 0, time.UTC), LatestFetchedAt: time.Date(2026, 1, 5, 8, 58, 0, 0, time.UTC)}
	must(t, status.Write(te.Dirs.File(paths.StatusFile), snap))
	te.out.Reset()
	eq(t, te.main("status", "--json"), 0)
	var got struct {
		Snapshot status.Snapshot `json:"snapshot"`
		Service  string          `json:"service"`
		Running  bool            `json:"running"`
	}
	must(t, json.Unmarshal(te.out.Bytes(), &got))
	eq(t, got.Snapshot.Mode, "coordinated")
	eq(t, got.Running, false)
	if got.Service == "" {
		t.Fatal("no service state")
	}

	te.out.Reset()
	eq(t, te.main("status"), 0)
	out := te.out.String()
	contains(t, out, "Claude Usage agent 0.0.0-dev")
	contains(t, out, "Mode:       coordinated, poller, server http://claude-usage.local")
	contains(t, out, "Weekly:     5%  Resets ")
	contains(t, out, "Backlog:    0")
}
