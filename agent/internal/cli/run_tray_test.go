package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
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
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayhelper"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// fakeServer records sync requests and answers as a standby with a short
// next sync.
type fakeServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []protocol.Request
	v2   []protocol.RequestV2
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sync" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req protocol.Request // schema 1, or the device part of schema 2
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		var v2 protocol.RequestV2
		if req.Schema == protocol.SchemaV2 {
			if err := json.Unmarshal(body, &v2); err != nil {
				http.Error(w, "bad", 422)
				return
			}
			for _, b := range v2.Providers { // recorded in schema 1 terms
				if b.Provider == "claude" {
					req.WantFresh = b.WantFresh
					req.Auth = protocol.AuthInfo{State: b.Auth.State, TokenExpiresAt: b.Auth.TokenExpiresAt}
				}
			}
		}
		fs.mu.Lock()
		fs.reqs = append(fs.reqs, req)
		if req.Schema == protocol.SchemaV2 {
			fs.v2 = append(fs.v2, v2)
		}
		fs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Schema != protocol.SchemaV2:
			fmt.Fprint(w, `{"role":"standby","fetch_now":false,"next_sync_seconds":15,"latest":null,"peers":{"devices":1,"poller_hostname":null},"server_urls":[],"client_latest_version":null}`)
		case v2.Event != nil:
			fmt.Fprint(w, `{"providers":[],"next_sync_seconds":60,"server_urls":[],"client_latest_version":null}`)
		default:
			fmt.Fprint(w, `{"providers":[{"provider":"claude","role":"standby","fetch_now":false,"latest":null,"peers":{"devices":1,"pcs":1,"poller_hostname":null},"problem":null}],"next_sync_seconds":15,"server_urls":[],"client_latest_version":null}`)
		}
	}))
	t.Cleanup(fs.Close)
	return fs
}

// v2requests are the schema 2 requests the server got.
func (fs *fakeServer) v2requests() []protocol.RequestV2 {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]protocol.RequestV2(nil), fs.v2...)
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

func (o onlyTo) SyncV2(ctx context.Context, base string, req protocol.RequestV2) (protocol.ResponseV2, error) {
	if base != o.base {
		return protocol.ResponseV2{}, errors.New("test: address refused")
	}
	return o.next.SyncV2(ctx, base, req)
}

// refuseAll is every test's syncer unless the test gives one: no test ever
// reaches a server outside the test.
type refuseAll struct{}

func (refuseAll) Sync(context.Context, string, protocol.Request) (protocol.Response, error) {
	return protocol.Response{}, errors.New("test: address refused")
}

func (refuseAll) SyncV2(context.Context, string, protocol.RequestV2) (protocol.ResponseV2, error) {
	return protocol.ResponseV2{}, errors.New("test: address refused")
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

// The engine syncs from the start, before any tray icon is up; the server
// hears "headless" until a helper shows its icon, then "tray" (design 6.4).
func TestRunTrayModeSyncsBeforeTrayReady(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.helpers.why = "no StatusNotifierWatcher" // a helper whose icon is not up
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	eq(t, modeOf(fs.requests()[0]), "headless")
	h.say(trayproto.Msg{Type: trayproto.TypeReady})
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "tray" })
	cancel()
	eq(t, waitCode(t, code), 0)
}

func TestRunTrayUnavailableFallsBackToHeadless(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.helpers.why = "no StatusNotifierWatcher"
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
	waitLog(t, te, "no tray (no StatusNotifierWatcher): running without an icon")
	cancel()
	eq(t, waitCode(t, code), 0)
	contains(t, te.err.String(), "no StatusNotifierWatcher")
}

// waitLog waits until agent.log has the text.
func waitLog(t *testing.T, te *testEnv, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(te.Dirs.File(paths.LogFile))
		if strings.Contains(string(b), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q not in agent.log:\n%s", want, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunShowsNoticeOnce(t *testing.T) {
	for goos, want := range map[string]string{
		"windows": "AI Usage is running. Its icon is under the ^ arrow on the taskbar; drag it onto the taskbar to keep it in view.",
		"linux":   "AI Usage is running. Its icon is in the top bar.",
		"darwin":  "AI Usage is running",
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
		var mu sync.Mutex
		te.helpers.onReady = func(h *fakeHelper) {
			// the notice comes before the first view: a helper shows it
			// once its icon is up (macOS: the menu bar title for a while)
			n := h.noticesGot()
			if goos == "darwin" {
				n = nil
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if v := h.lastView(); v.Title == want {
						n = []string{want}
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			mu.Lock()
			got = append(got, strings.Join(n, ""))
			mu.Unlock()
			h.click("quit")
		}
		eq(t, te.main("run"), 0)
		eq(t, te.main("run"), 0)
		mu.Lock()
		eq(t, got, []string{want, ""})
		mu.Unlock()
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: marker still there", goos)
		}
	}
}

func TestHeadlessRunDropsNoticeMarker(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
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
	eq(t, te.helpers.count(), 0) // a headless run starts no helper
}

func TestLinuxWithoutDesktopRunsHeadless(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux" // no XDG_* variables
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	eq(t, modeOf(fs.requests()[0]), "headless")
	cancel()
	eq(t, waitCode(t, code), 0)
	eq(t, te.helpers.count(), 0)
}

func TestControllerRefreshSendsWantFresh(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	h.click("refresh")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 2 && r[len(r)-1].WantFresh })
	cancel()
	eq(t, waitCode(t, code), 0)
}

func TestControllerDashboardURL(t *testing.T) {
	cfg := mustParse(t, `{"server_urls":["http://ai-usage.local","http://192.0.2.11"],"ingest_key":"k-0123456789"}`)
	c := newController(cfg, status.Snapshot{ServerURL: "http://192.0.2.10:8700"}, nil, nil)
	eq(t, c.DashboardURL(), "http://192.0.2.10:8700")
	c = newController(cfg, status.Snapshot{}, nil, nil)
	eq(t, c.DashboardURL(), "http://ai-usage.local")
	eq(t, c.Snapshot().Mode, "starting")
	eq(t, c.HasToggle(), false)
}

func TestQuitExitsZeroAndUnlocks(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.helpers.autoClick = "quit"
	eq(t, te.main("run"), 0)
	unlock, err := procs.Lock(te.Dirs.File(paths.LockFile))
	must(t, err)
	unlock()
}

// A helper that cannot start leaves the agent collecting without an icon.
func TestTrayErrorFallsBackToHeadless(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.helpers.fail = errors.New("tray broke")
	cancel, code := te.mainAsync("run")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool {
		return len(r) > 0 && modeOf(r[len(r)-1]) == "headless"
	})
	waitLog(t, te, "tray broke")
	cancel()
	eq(t, waitCode(t, code), 0)
	contains(t, te.err.String(), "tray broke")
}

func TestVersionOutputNamesTray(t *testing.T) {
	te := newEnv(t)
	eq(t, te.main("version"), 0)
	re := regexp.MustCompile(`^ai-usage 0\.0\.0-dev [a-z0-9]+/[a-z0-9]+ (tray|no-tray)\n$`)
	if !re.MatchString(te.out.String()) {
		t.Fatalf("%q", te.out.String())
	}
	if trayhelper.Compiled != strings.HasSuffix(te.out.String(), " tray\n") {
		t.Fatal("tray word does not follow trayhelper.Compiled")
	}
}

func TestStatusShowsClientMode(t *testing.T) {
	te := newEnv(t)
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated", ClientMode: "tray", Providers: claudeSnap(status.ProviderStatus{State: "ok", Role: "standby"})}))
	eq(t, te.main("status"), 0)
	contains(t, te.out.String(), "Mode:       coordinated (tray)\n")
	contains(t, te.out.String(), "Claude:     signed in · standby\n") // the role is per provider (design 6.12)
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
	cancel, code := te.mainAsync("run")
	defer cancel()
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	h.click("remove")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(itemText(h.lastView(), "remove"), "again") {
		if time.Now().After(deadline) {
			t.Fatalf("Remove not armed: %q", itemText(h.lastView(), "remove"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.click("remove") // the second click removes
	eq(t, waitCode(t, code), 0)
	for _, p := range []string{te.Dirs.Data, te.Dirs.InstallExe} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left", p)
		}
	}
	calls := rec.list()
	if !slices.Contains(calls, "main.unregister") || !slices.Contains(calls, "winapp.unregister") {
		t.Fatalf("%v", calls)
	}
	// the helper was asked to quit before anything was removed
	select {
	case <-h.ended:
		eq(t, h.code, 0)
	default:
		t.Fatal("the helper is still running")
	}
}

func TestMacLogIsCutAtStart(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "darwin"
	te.helpers.autoClick = "quit"
	logPath := te.Dirs.File(paths.LogFile) // launchd appends here
	must(t, os.WriteFile(logPath, make([]byte, 2<<20), 0o600))
	eq(t, te.main("run"), 0)
	st, err := os.Stat(logPath)
	must(t, err)
	if st.Size() > 1<<20 {
		t.Fatalf("agent.log is still %d bytes", st.Size())
	}
}

// A helper that waited for its tray host and then shows its icon switches
// the agent to "tray".
func TestLateTrayHostIsPickedUp(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.helpers.why = "no StatusNotifierWatcher"
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "headless" })
	h.say(trayproto.Msg{Type: trayproto.TypeReady})
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > 0 && modeOf(r[len(r)-1]) == "tray" })
	waitLog(t, te, "tray found: showing the icon")
	cancel()
	eq(t, waitCode(t, code), 0)
}

// CI builds can be told to show a second helper (testHelpers); any other
// build ignores the switch.
func TestTestHelpersOnlyInCIBuilds(t *testing.T) {
	te := newEnv(t)
	te.vars[testHelpersEnv] = "claude, codex,codex,other"
	eq(t, len(testHelpers(te.Env)), 0)
	asCIBuild(t)
	eq(t, testHelpers(te.Env), []provider.ID{provider.Claude, provider.Codex})
}

// Two helpers at once (a fake second provider): both get the same menu,
// each its own icon; the one not signed in shows the neutral ring; a click
// in either is handled; both end with the agent.
func TestTwoHelpersShowOneMenu(t *testing.T) {
	asCIBuild(t)
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.vars[testHelpersEnv] = "claude,codex"
	cancel, code := te.mainAsync("run")
	a, b := te.helpers.ready(), te.helpers.ready()
	ids := map[provider.ID]*fakeHelper{a.id: a, b.id: b}
	n, c := ids[provider.Claude], ids[provider.Codex]
	if n == nil || c == nil || n.uid != 100 || c.uid != 101 {
		t.Fatalf("helpers %q/%d %q/%d", a.id, a.uid, b.id, b.uid)
	}
	eq(t, c.lastView().Icon.Provider, "") // nobody signed in: rings
	eq(t, n.lastView().Icon.Provider, "")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	// both get the same menu (each change reaches both)
	deadline := time.Now().Add(5 * time.Second)
	for !reflect.DeepEqual(c.lastView().Items, n.lastView().Items) || itemText(n.lastView(), "server") == text.Connecting {
		if time.Now().After(deadline) {
			t.Fatalf("menus differ:\n%+v\n%+v", c.lastView().Items, n.lastView().Items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := len(fs.requests())
	c.click("refresh")
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) > before && r[len(r)-1].WantFresh })
	cancel()
	eq(t, waitCode(t, code), 0)
	for _, h := range []*fakeHelper{n, c} {
		select {
		case <-h.ended:
		case <-time.After(5 * time.Second):
			t.Fatalf("helper %q still running", h.id)
		}
	}
}

// asCIBuild makes testHelpers see a CI build for this test.
func asCIBuild(t *testing.T) {
	old := buildVersion
	buildVersion = func() string { return "0.0.0-ci" }
	t.Cleanup(func() { buildVersion = old })
}
