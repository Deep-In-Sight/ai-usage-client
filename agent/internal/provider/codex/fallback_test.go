package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	cx "github.com/Deep-In-Sight/ai-usage-client/agent/internal/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
)

var fakeCodexBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakecodex")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	fakeCodexBin = filepath.Join(dir, "codex")
	if runtime.GOOS == "windows" {
		fakeCodexBin += ".exe"
	}
	cmd := exec.Command(goBin, "build", "-o", fakeCodexBin, "../../../tools/fakecodex")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building fakecodex: %v: %s\n", err, b)
		os.RemoveAll(dir)
		os.Exit(2)
	}
	cx.AllowCLIRuns(true) // only a CI build's test switch does this in the agent (ruling F1)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func fakeCodex(t *testing.T, flags ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), filepath.Base(fakeCodexBin))
	if err := os.Link(fakeCodexBin, p); err != nil {
		b, err := os.ReadFile(fakeCodexBin)
		must(t, err)
		must(t, os.WriteFile(p, b, 0o755))
	}
	must(t, os.WriteFile(p+".args", []byte(strings.Join(flags, "\n")+"\n"), 0o600))
	return p
}

type starts struct{ n int }

func (s *starts) start(cmd *exec.Cmd) (*procs.Group, error) { s.n++; return procs.StartGroup(cmd) }

// realFallback is the shipped AppServer running a fakecodex.
func realFallback(t *testing.T, clk clock.Clock, cli string) (*cx.AppServer, *starts) {
	t.Helper()
	st := &starts{}
	f := &cx.CLIFinder{List: func() []string { return []string{cli} }, Clock: clk}
	f.CLI()
	return &cx.AppServer{CLIs: f, Home: t.TempDir(), Clock: clk, Version: "0.0.0-test", Start: st.start}, st
}

var appServerOK = provider.Result{Provider: provider.Codex, HTTPStatus: 200, Source: "app-server",
	Usage: json.RawMessage(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":5,"windowDurationMins":10080,"resetsAt":1768046400}}}`)}

// After the fallback answered for an unusable direct request, the
// fallback is asked first and chatgpt.com is not asked again for 6 hours
// (design D1); then the direct request is tried again, and once it works
// it is first again.
func TestFallbackFirstForSixHours(t *testing.T) {
	s := newServer(t)
	s.set(403, `<html>blocked</html>`)
	fb := &fallback{res: appServerOK, ok: true}
	p, home, clk := newProvider(t, s, fb)
	signIn(t, home, t0.Add(9*24*time.Hour))
	ctx := context.Background()
	p.Login(ctx)
	res, err := p.Fetch(ctx)
	must(t, err)
	eq(t, res.Source, "app-server")
	eq(t, s.count(), 1)
	for i := 0; i < 3; i++ {
		clk.Advance(time.Hour)
		res, err = p.Fetch(ctx)
		must(t, err)
		eq(t, res.Source, "app-server")
	}
	eq(t, s.count(), 1)
	eq(t, fb.calls, 4)
	clk.Advance(3 * time.Hour) // 6 h after the switch
	s.set(200, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":604800,"reset_at":1768046400}}}`)
	res, err = p.Fetch(ctx)
	must(t, err)
	eq(t, res.Source, "wham")
	eq(t, s.count(), 2)
	clk.Advance(time.Hour)
	res, _ = p.Fetch(ctx)
	eq(t, res.Source, "wham")
	eq(t, fb.calls, 4)
}

// A fallback that cannot run now (token near its expiry, no CLI, a start
// less than 10 minutes ago) is not asked, and the direct request keeps
// its own 30 s.
func TestFallbackNotAskedWhenNotReady(t *testing.T) {
	s := newServer(t)
	s.set(403, `x`)
	fb := &fallback{res: appServerOK, ok: true, notReady: true}
	p, home, _ := newProvider(t, s, fb)
	p.DirectLimit = time.Millisecond // would cut the request if it were used
	signIn(t, home, t0.Add(9*24*time.Hour))
	p.Login(context.Background())
	res, err := p.Fetch(context.Background())
	must(t, err)
	eq(t, res.HTTPStatus, 403)
	eq(t, fb.calls, 0)
}

// When the fallback could run, the direct request gets DirectLimit of the
// budget; a direct request cut by it is no reason for the fallback.
func TestDirectRequestShorterWhenTheFallbackCouldRun(t *testing.T) {
	s := newServer(t)
	s.delay = 2 * time.Second
	fb := &fallback{res: appServerOK, ok: true}
	p, home, _ := newProvider(t, s, fb)
	p.DirectLimit = 200 * time.Millisecond
	signIn(t, home, t0.Add(9*24*time.Hour))
	p.Login(context.Background())
	start := time.Now()
	res, err := p.Fetch(context.Background())
	must(t, err)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v", took)
	}
	eq(t, res.HTTPStatus, 0)
	eq(t, fb.calls, 0)
}

// Codex's own "authentication required" counts as a refused sign-in: no
// more requests or starts until auth.json changes.
func TestFallback401MarksExpired(t *testing.T) {
	s := newServer(t)
	s.set(404, `{}`)
	fb := &fallback{res: provider.Result{Provider: provider.Codex, HTTPStatus: 401, Source: "app-server"}, ok: true}
	p, home, _ := newProvider(t, s, fb)
	signIn(t, home, t0.Add(9*24*time.Hour))
	ctx := context.Background()
	p.Login(ctx)
	res, err := p.Fetch(ctx)
	must(t, err)
	eq(t, res.HTTPStatus, 401)
	eq(t, p.Login(ctx).State, "expired")
	if _, err := p.Fetch(ctx); err == nil {
		t.Fatal("fetched after codex's 401")
	}
	eq(t, fb.calls, 1)
	eq(t, s.count(), 1)
}

// RF2 through the provider with the shipped fallback: a 403 with a token
// 20 minutes from its expiry starts no codex; 31 minutes does.
func TestNearExpiryStartsNoCodex(t *testing.T) {
	for _, c := range []struct {
		left   time.Duration
		starts int
	}{{20 * time.Minute, 0}, {31 * time.Minute, 1}} {
		s := newServer(t)
		s.set(403, `x`)
		clk := clock.NewFake(t0)
		fb, st := realFallback(t, clk, fakeCodex(t))
		home := t.TempDir()
		p := New(cx.NewReader(home, clk), cx.NewClientWith(cx.DefaultBaseURL, "ai-usage/0.0.0-test", clk, s.transport()), fb)
		signIn(t, home, t0.Add(c.left))
		p.Login(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), engine.FetchTimeout)
		res, err := p.Fetch(ctx)
		cancel()
		must(t, err)
		eq(t, st.n, c.starts)
		if c.starts == 1 {
			eq(t, res.Source, "app-server")
			eq(t, res.HTTPStatus, 200)
		} else {
			eq(t, res.HTTPStatus, 403)
		}
	}
}

// R1 at full scale: the worst Codex fetch — a direct request answered
// with 403 just before its 15 s limit, then a codex that never answers,
// ignores SIGTERM, does not end when its input closes and has a child —
// ends inside the engine's 35 s cap, with codex and its child ended and
// reaped. (So two fetches and the sync still fit the server's 90 s:
// TestWorstCaseFitsTheServersFetchTimeout.)
func TestCodexFetchEndsInsideTheEngineCap(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 35 s")
	}
	s := newServer(t)
	s.set(403, `x`)
	s.delay = DirectLimitWithFallback - 500*time.Millisecond
	clk := clock.NewFake(t0)
	pidfile := filepath.Join(t.TempDir(), "pids")
	fb, st := realFallback(t, clk, fakeCodex(t, "-mode", "child", "-ignore-term", "-pidfile", pidfile))
	home := t.TempDir()
	p := New(cx.NewReader(home, clk), cx.NewClientWith(cx.DefaultBaseURL, "ai-usage/0.0.0-test", clk, s.transport()), fb)
	signIn(t, home, t0.Add(9*24*time.Hour))
	p.Login(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), engine.FetchTimeout)
	defer cancel()
	start := time.Now()
	res, err := p.Fetch(ctx)
	took := time.Since(start)
	must(t, err)
	eq(t, st.n, 1)
	eq(t, res.Source, "app-server")
	eq(t, res.HTTPStatus, 0)
	if took >= engine.FetchTimeout {
		t.Fatalf("the fetch took %v, the cap is %v", took, engine.FetchTimeout)
	}
	b, err := os.ReadFile(pidfile)
	must(t, err)
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		n, _ := strconv.Atoi(f)
		pids = append(pids, n)
	}
	eq(t, len(pids), 2)
	for end := time.Now().Add(2 * time.Second); ; {
		alive := 0
		for _, pid := range pids {
			if processAlive(pid) {
				alive++
			}
		}
		if alive == 0 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("%d processes left after the fetch", alive)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("worst Codex fetch: %v (cap %v)", took.Round(10*time.Millisecond), engine.FetchTimeout)
}
