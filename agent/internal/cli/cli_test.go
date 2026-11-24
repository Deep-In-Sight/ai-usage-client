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

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayhelper"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/winapp"
)

// fakeMachineKey stands for this PC's machine key in tests.
const fakeMachineKey = "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"

const validConfig = `{"server_urls":["http://ai-usage.local","http://192.0.2.10:8700"],"ingest_key":"test-ingest-key-0123456789"}`

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
	helpers  *fakeHelpers // the tray helpers a run starts
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
	exe := filepath.Join(exeDir, "ai-usage-download")
	must(t, os.WriteFile(exe, []byte("BIN"), 0o755))
	te.Env = Env{
		Getenv: getenv, Stdout: te.out, Stderr: te.err, Exe: exe, Dirs: dirs, Run: te.runner.run,
		StartDetached: func(exe string, args ...string) error {
			te.detached = append(te.detached, append([]string{exe}, args...))
			return nil
		},
		Clock: clock.NewFake(time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)),
	}
	te.Env.Autostart = func(headless bool) (autostart.Method, error) {
		return autostart.For(autostartOptions(dirs, headless, te.runner.run, te.Env.StartDetached))
	}
	te.Env.Others = func(headless bool) []autostart.Method {
		return autostart.Others(autostartOptions(dirs, headless, te.runner.run, te.Env.StartDetached))
	}
	te.Env.WinApp = noWinApp{}
	te.Env.Machine = func() (string, string) { return fakeMachineKey, "os" }
	te.Env.Syncer = func(config.Config, string) engine.Syncer { return refuseAll{} }
	te.helpers = newFakeHelpers(t)
	te.Env.Helper = te.helpers.spawn
	te.Env.OpenURL = func(string) error { return nil }
	return te
}

func (te *testEnv) main(args ...string) int {
	te.Args = args
	return Main(te.Env)
}

func TestVersion(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("version"), 0)
	word := "no-tray"
	if trayhelper.Compiled {
		word = "tray"
	}
	eq(t, te.out.String(), "ai-usage 0.0.0-dev "+runtime.GOOS+"/"+runtime.GOARCH+" "+word+"\n")
}

// The third-party licence notices ship inside the binary.
func TestLicenses(t *testing.T) {
	for _, cmd := range []string{"licenses", "licences"} {
		te := newEnv(t)
		eq(t, te.main(cmd), 0)
		contains(t, te.out.String(), "fyne.io/systray")
		contains(t, te.out.String(), "Apache License")
		contains(t, te.out.String(), runtime.Version())
	}
	eq(t, printsOutput([]string{"licenses"}), true) // Windows: to the console it was started from
	te := newEnv(t)
	te.main("help")
	contains(t, te.out.String(), "licenses")
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

func claudeSnap(c status.ProviderStatus) map[provider.ID]status.ProviderStatus {
	return map[provider.ID]status.ProviderStatus{provider.Claude: c}
}

func TestStatusJSON(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("status"), 1) // no status file yet
	pct := 5.0
	resets := time.Date(2026, 1, 9, 6, 0, 0, 0, time.UTC)
	snap := status.Snapshot{Version: "0.0.0-dev", PID: 1, Mode: "coordinated", ServerURL: "http://ai-usage.local",
		LastSyncAt: time.Date(2026, 1, 5, 8, 59, 0, 0, time.UTC), LastSyncOK: true,
		Providers: claudeSnap(status.ProviderStatus{State: "ok", Role: "poller", ExpiresAt: time.Date(2026, 1, 5, 17, 0, 0, 0, time.UTC),
			Rows: []provider.Row{{Key: "weekly_all", Label: "Weekly", Percent: &pct, ResetsAt: &resets}}, MainKey: "weekly_all",
			LatestFetchedAt: time.Date(2026, 1, 5, 8, 58, 0, 0, time.UTC)})}
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
	eq(t, got.Snapshot.Providers[provider.Claude].Role, "poller")
	eq(t, got.Running, false)
	if got.Service == "" {
		t.Fatal("no service state")
	}

	te.out.Reset()
	eq(t, te.main("status"), 0)
	out := te.out.String()
	contains(t, out, "AI Usage agent 0.0.0-dev")
	contains(t, out, "Mode:       coordinated, server http://ai-usage.local")
	contains(t, out, "Claude:     signed in, expires ")
	contains(t, out, " · poller\n")
	contains(t, out, "            Weekly 5%  Resets ")
	contains(t, out, "Backlog:    0")
}

// The status text in the layout of design 6.12, Claude part (golden).
func TestStatusGolden(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = old })
	te := newEnv(t)
	at := func(h, m int) time.Time { return time.Date(2026, 1, 5, h, m, 0, 0, time.UTC) }
	ptr := func(v time.Time) *time.Time { return &v }
	st := 200
	snap := status.Snapshot{Version: "0.0.0-dev", PID: 1, Mode: "coordinated", ServerURL: "http://ai-usage.local", ClientMode: "tray",
		LastSyncAt: at(8, 59), LastSyncOK: true, Backlog: 2,
		Providers: claudeSnap(status.ProviderStatus{State: "ok", Role: "standby", PollerHostname: "TEST-PC-03", ExpiresAt: at(17, 30),
			LatestFetchedAt: at(8, 58), LastFetchAt: at(8, 40), LastFetchStatus: &st, MainKey: "weekly_all",
			Rows: []provider.Row{
				{Key: "five_hour", Label: "5-hour", Percent: pf(100), ResetsAt: ptr(at(14, 20))},
				{Key: "weekly_all", Label: "Weekly", Percent: pf(30), ResetsAt: ptr(time.Date(2026, 1, 9, 6, 0, 0, 0, time.UTC))},
				{Key: "weekly_scoped:Fable", Label: "Fable", Percent: pf(25)},
			}})}
	must(t, status.Write(te.Dirs.File(paths.StatusFile), snap))
	eq(t, te.main("status"), 0)
	out := te.out.String()
	_, after, _ := strings.Cut(out, "Mode:") // Service and Process depend on the host
	want := `       coordinated, server http://ai-usage.local (tray)
Machine:    identified (OS machine id)
Last sync:  08:59 ok
Claude:     signed in, expires 17:30 · standby (TEST-PC-03)
            5-hour 100%  Resets Mon 14:20 · in 5h 20m
            Weekly  30%  Resets Fri 06:00 · in 3d 21h
            Fable   25%
            Updated 08:58 · last fetch 08:40 HTTP 200
Backlog:    2
`
	if !strings.HasPrefix(after, want) {
		t.Fatalf("got\n%s\nwant\n%s", after, want)
	}
	// signed out, an expired sign-in with a problem, a reply that could not be read
	set := func(c status.ProviderStatus) string {
		snap.Providers = claudeSnap(c)
		must(t, status.Write(te.Dirs.File(paths.StatusFile), snap))
		te.out.Reset()
		eq(t, te.main("status"), 0)
		return te.out.String()
	}
	contains(t, set(status.ProviderStatus{State: "not_signed_in", Rows: []provider.Row{{Key: "weekly_all", Label: "Weekly"}}}),
		"Claude:     not signed in\n            Weekly –%\nBacklog:")
	contains(t, set(status.ProviderStatus{State: "expired", ExpiresAt: time.Date(2026, 1, 4, 23, 0, 0, 0, time.UTC), Problem: "x"}),
		"Claude:     sign-in expired, expires Jan 4 23:00 (x)\n")
	contains(t, set(status.ProviderStatus{State: "ok", ParseProblem: "no_weekly", Role: "poller"}),
		"Claude:     signed in · poller\n            Usage reply could not be read\n")
}

func pf(v float64) *float64 { return &v }

// status says whether the PC is identified and how, never the key.
func TestStatusNamesTheMachineKeySource(t *testing.T) {
	te := newEnv(t)
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated"}))
	for src, want := range map[string]string{"os": "identified (OS machine id)", "": "not identified (this OS gives no machine identifier; a reinstall counts as a new PC)"} {
		te.Machine = func() (string, string) {
			if src == "" {
				return "", ""
			}
			return fakeMachineKey, src
		}
		te.out.Reset()
		eq(t, te.main("status"), 0)
		contains(t, te.out.String(), "Machine:    "+want+"\n")
		te.out.Reset()
		eq(t, te.main("status", "--json"), 0)
		if strings.Contains(te.out.String(), fakeMachineKey) {
			t.Fatal("the machine key is printed")
		}
	}
}

func TestStatusShowsSignInProblem(t *testing.T) {
	te := newEnv(t)
	snap := status.Snapshot{Version: "0.0.0-dev", PID: 1, Mode: "coordinated",
		Providers: claudeSnap(status.ProviderStatus{State: "ok", Problem: "account file larger than 32 MiB"})}
	must(t, status.Write(te.Dirs.File(paths.StatusFile), snap))
	eq(t, te.main("status"), 0)
	contains(t, te.out.String(), "Claude:     signed in (account file larger than 32 MiB)")
}

// The status file a v0.8.1 agent wrote shows its Claude numbers: status
// run right after an upgrade, before the new agent's first step.
func TestStatusReadsAV081File(t *testing.T) {
	te := newEnv(t)
	b, err := os.ReadFile("../testdata/v081/status.json")
	must(t, err)
	must(t, os.MkdirAll(te.Dirs.Data, 0o700))
	must(t, os.WriteFile(te.Dirs.File(paths.StatusFile), b, 0o600))
	eq(t, te.main("status"), 0)
	contains(t, te.out.String(), "Claude:     signed in, expires ")
	contains(t, te.out.String(), "            Weekly 30%  ")
	contains(t, te.out.String(), "            Fable  76%  ")
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	must(t, err)
	return string(b)
}

func writeCfg(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	must(t, os.WriteFile(p, []byte(s), 0o600))
	return p
}

// noWinApp keeps tests away from the real registry.
type noWinApp struct{}

func (noWinApp) Register(winapp.Entry) error { return nil }
func (noWinApp) Unregister() error           { return nil }
