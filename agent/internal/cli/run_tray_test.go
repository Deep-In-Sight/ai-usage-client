package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/clock"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/tray"
)

// fakeServer records sync requests and answers as a standby with a short
// next sync.
type fakeServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []protocol.Request
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sync" {
			http.NotFound(w, r)
			return
		}
		var req protocol.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		fs.mu.Lock()
		fs.reqs = append(fs.reqs, req)
		fs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"role":"standby","fetch_now":false,"next_sync_seconds":15,"latest":null,"peers":{"devices":1,"poller_hostname":null},"server_urls":[],"client_latest_version":null}`)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fakeServer) requests() []protocol.Request {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]protocol.Request(nil), fs.reqs...)
}

func (fs *fakeServer) waitFor(t *testing.T, limit time.Duration, cond func([]protocol.Request) bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond(fs.requests()) {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v; %d requests", limit, len(fs.requests()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// onlyTo is a syncer that refuses every address but the fake server's, so
// a test never reaches a real server found by mDNS.
type onlyTo struct {
	base string
	next engine.Syncer
}

func (o onlyTo) Sync(ctx context.Context, base string, req protocol.Request) (protocol.Response, error) {
	if base != o.base {
		return protocol.Response{}, errors.New("test: address refused")
	}
	return o.next.Sync(ctx, base, req)
}

// trayEnv prepares a run in tray mode against a fake server. There is no
// sign-in on purpose: the agent never fetches from Anthropic in these tests.
func trayEnv(t *testing.T, fs *fakeServer) *testEnv {
	t.Helper()
	te := newEnv(t)
	te.Clock = clock.Real{}
	te.GOOS = "windows"
	cfg := mustParse(t, `{"server_urls":["`+fs.URL+`"],"ingest_key":"test-ingest-key-0123456789"}`)
	must(t, config.Save(te.Dirs.File(paths.ConfigFile), cfg))
	te.Syncer = func(cfg config.Config, ua string) engine.Syncer {
		return onlyTo{base: fs.URL, next: protocol.NewClient(cfg.IngestKey, ua, nil)}
	}
	te.TrayAvailable = func(context.Context) (bool, string) { return true, "" }
	return te
}

// mainAsync runs Main in a goroutine; cancel stops it like SIGTERM.
func (te *testEnv) mainAsync(args ...string) (cancel func(), code <-chan int) {
	ctx, stop := context.WithCancel(context.Background())
	te.Context = ctx
	te.Args = args
	ch := make(chan int, 1)
	go func() { ch <- Main(te.Env) }()
	return stop, ch
}

func waitCode(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("Main did not return")
		return -1
	}
}

func modeOf(r protocol.Request) string { return string(r.Client.Mode) }

func TestRunTrayModeSyncsBeforeTrayReady(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	trayCalled := make(chan struct{})
	te.Tray = func(ctx context.Context, c tray.Controller, updates <-chan struct{}, notice string) error {
		close(trayCalled)
		<-ctx.Done() // a tray that never becomes ready
		return nil
	}
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	eq(t, modeOf(fs.requests()[0]), "tray")
	<-trayCalled
	cancel()
	eq(t, waitCode(t, code), 0)
}

func TestRunTrayUnavailableFallsBackToHeadless(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.TrayAvailable = func(context.Context) (bool, string) { return false, "no StatusNotifierWatcher" }
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error {
		t.Error("Tray called")
		return nil
	}
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool {
		return len(r) > 0 && modeOf(r[len(r)-1]) == "headless"
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := status.Read(te.Dirs.File(paths.StatusFile))
		if err == nil && s.ClientMode == "headless" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status.json client_mode: %+v %v", s, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	eq(t, waitCode(t, code), 0)
	contains(t, te.err.String(), "no StatusNotifierWatcher")
}

func TestRunShowsNoticeOnce(t *testing.T) {
	for goos, want := range map[string]string{
		"windows": "Claude Usage is running. Its icon is under the ^ arrow on the taskbar; drag it onto the taskbar to keep it in view.",
		"linux":   "Claude Usage is running. Its icon is in the top bar.",
		"darwin":  "Claude Usage is running",
	} {
		fs := newFakeServer(t)
		te := trayEnv(t, fs)
		te.GOOS = goos
		if goos == "linux" {
			te.vars["XDG_SESSION_TYPE"] = "wayland"
		}
		marker := te.Dirs.File(paths.NotifyFile)
		must(t, os.WriteFile(marker, []byte("1.0.0"), 0o600))
		var got []string
		te.Tray = func(ctx context.Context, c tray.Controller, u <-chan struct{}, notice string) error {
			got = append(got, notice)
			return nil // Quit at once
		}
		eq(t, te.main("run"), 0)
		eq(t, te.main("run"), 0)
		eq(t, got, []string{want, ""})
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: marker still there", goos)
		}
	}
}

func TestHeadlessRunDropsNoticeMarker(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error {
		t.Error("Tray called")
		return nil
	}
	marker := te.Dirs.File(paths.NotifyFile)
	must(t, os.WriteFile(marker, []byte("1.0.0"), 0o600))
	cancel, code := te.mainAsync("run", "--headless")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	eq(t, modeOf(fs.requests()[0]), "headless")
	cancel()
	eq(t, waitCode(t, code), 0)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker still there")
	}
}

func TestLinuxWithoutDesktopRunsHeadless(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux" // no XDG_* variables
	te.TrayAvailable = func(context.Context) (bool, string) {
		t.Error("TrayAvailable called")
		return false, ""
	}
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	eq(t, modeOf(fs.requests()[0]), "headless")
	cancel()
	eq(t, waitCode(t, code), 0)
}

func TestControllerRefreshSendsWantFresh(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	ctrlCh := make(chan tray.Controller, 1)
	te.Tray = func(ctx context.Context, c tray.Controller, u <-chan struct{}, notice string) error {
		ctrlCh <- c
		<-ctx.Done()
		return nil
	}
	cancel, code := te.mainAsync("run")
	c := <-ctrlCh
	eq(t, c.IconDir(), te.Dirs.File(paths.TrayIconsDir))
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	c.Refresh()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 2 && r[len(r)-1].WantFresh })
	cancel()
	eq(t, waitCode(t, code), 0)
}

func TestControllerDashboardURL(t *testing.T) {
	cfg := mustParse(t, `{"server_urls":["http://claude-usage.local","http://192.0.2.11"],"ingest_key":"k-0123456789"}`)
	c := newController(cfg, status.Snapshot{ServerURL: "http://192.0.2.10:8700"}, nil, nil)
	eq(t, c.DashboardURL(), "http://192.0.2.10:8700")
	c = newController(cfg, status.Snapshot{}, nil, nil)
	eq(t, c.DashboardURL(), "http://claude-usage.local")
	eq(t, c.Snapshot().Mode, "starting")
	eq(t, c.HasToggle(), false)
	if err := c.Remove(); err == nil {
		t.Fatal("Remove should not be available yet")
	}
}

func TestQuitExitsZeroAndUnlocks(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error { return nil }
	eq(t, te.main("run"), 0)
	unlock, err := procs.Lock(te.Dirs.File(paths.LockFile))
	must(t, err)
	unlock()
}

func TestTrayErrorFallsBackToHeadless(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error { return errors.New("tray broke") }
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool {
		return len(r) > 0 && modeOf(r[len(r)-1]) == "headless"
	})
	cancel()
	eq(t, waitCode(t, code), 0)
	contains(t, te.err.String(), "tray broke")
}

func TestVersionOutputNamesTray(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("version"), 0)
	re := regexp.MustCompile(`^claude-usage 0\.0\.0-dev [a-z0-9]+/[a-z0-9]+ (tray|no-tray)\n$`)
	if !re.MatchString(te.out.String()) {
		t.Fatalf("%q", te.out.String())
	}
	if tray.Compiled != strings.HasSuffix(te.out.String(), " tray\n") {
		t.Fatal("tray word does not follow tray.Compiled")
	}
}

func TestStatusShowsClientMode(t *testing.T) {
	te := newEnv(t)
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated", Role: "standby", ClientMode: "tray", AuthState: "ok"}))
	eq(t, te.main("status"), 0)
	contains(t, te.out.String(), "Mode:       coordinated, standby (tray)")
}

func TestTrayRemoveRemovesEverything(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	rec := &recorder{}
	main := &fakeMethod{name: "main", rec: rec, exe: te.Dirs.InstallExe}
	te.Autostart = func(bool) (autostart.Method, error) { return main, nil }
	te.Others = nil
	te.WinApp = &fakeWinApp{rec: rec}
	must(t, os.MkdirAll(filepath.Dir(te.Dirs.InstallExe), 0o755))
	must(t, os.WriteFile(te.Dirs.InstallExe, []byte("BIN"), 0o755))
	te.Tray = func(ctx context.Context, c tray.Controller, u <-chan struct{}, notice string) error {
		fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
		return c.Remove() // then Quit
	}
	eq(t, te.main("run"), 0)
	for _, p := range []string{te.Dirs.Data, te.Dirs.InstallExe} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left", p)
		}
	}
	calls := rec.list()
	if !slices.Contains(calls, "main.unregister") || !slices.Contains(calls, "winapp.unregister") {
		t.Fatalf("%v", calls)
	}
}

func TestMacLogIsCutAtStart(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "darwin"
	te.Tray = func(context.Context, tray.Controller, <-chan struct{}, string) error { return nil }
	logPath := te.Dirs.File(paths.LogFile) // launchd appends here
	must(t, os.WriteFile(logPath, make([]byte, 2<<20), 0o600))
	eq(t, te.main("run"), 0)
	st, err := os.Stat(logPath)
	must(t, err)
	if st.Size() > 1<<20 {
		t.Fatalf("agent.log is still %d bytes", st.Size())
	}
}
