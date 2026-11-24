package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// counter wraps procs.StartGroup and counts the starts.
type counter struct {
	mu   sync.Mutex
	n    int
	cmds []*exec.Cmd
}

func (c *counter) start(cmd *exec.Cmd) (*procs.Group, error) {
	c.mu.Lock()
	c.n++
	c.cmds = append(c.cmds, cmd)
	c.mu.Unlock()
	return procs.StartGroup(cmd)
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// appServer runs the fakecodex at cli with a short budget so that a
// whole run fits a few seconds (the real one: TestCodexFetchEndsInsideTheEngineCap
// in provider/codex).
func appServer(t *testing.T, clk clock.Clock, cli string) (*AppServer, *counter) {
	t.Helper()
	c := &counter{}
	f := &CLIFinder{List: func() []string { return []string{cli} }, Clock: clk}
	f.CLI()
	return &AppServer{CLIs: f, Home: t.TempDir(), Clock: clk, Version: "0.0.0-test", Start: c.start,
		MinCall: 300 * time.Millisecond, ExitGrace: 500 * time.Millisecond, ReapMargin: 300 * time.Millisecond}, c
}

func authUntil(exp time.Time) Auth {
	return Auth{Mode: "chatgpt", AccountID: fakeAccountID, ExpiresAt: exp}
}

func budget(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func TestCanSpawn(t *testing.T) {
	eq(t, CanSpawn(authUntil(t0.Add(20*time.Minute)), t0), false)
	eq(t, CanSpawn(authUntil(t0.Add(30*time.Minute)), t0), false)
	eq(t, CanSpawn(authUntil(t0.Add(31*time.Minute)), t0), true)
	eq(t, CanSpawn(Auth{}, t0), false)
	eq(t, CanSpawn(authUntil(t0.Add(-time.Hour)), t0), false)
}

// RF2: a token 20 minutes from its expiry never starts codex (the codex it
// would start could renew it); 31 minutes does, once.
func TestFallbackNeverSpawnsNearExpiry(t *testing.T) {
	clk := clock.NewFake(t0)
	s, c := appServer(t, clk, fakeCodex(t))
	ctx, cancel := budget(5 * time.Second)
	defer cancel()
	near := authUntil(t0.Add(20 * time.Minute))
	eq(t, s.Ready(near), false)
	if _, ok := s.Fetch(ctx, near); ok {
		t.Fatal("attempted 20 minutes before the expiry")
	}
	eq(t, c.count(), 0)
	far := authUntil(t0.Add(31 * time.Minute))
	eq(t, s.Ready(far), true)
	res, ok := s.Fetch(ctx, far)
	eq(t, ok, true)
	eq(t, res.HTTPStatus, 200)
	eq(t, c.count(), 1)
}

func TestFallbackNeverSpawnsWithoutExp(t *testing.T) {
	clk := clock.NewFake(t0)
	s, c := appServer(t, clk, fakeCodex(t))
	ctx, cancel := budget(5 * time.Second)
	defer cancel()
	a := authUntil(time.Time{})
	a.LastRefresh = t0
	eq(t, s.Ready(a), false)
	if _, ok := s.Fetch(ctx, a); ok {
		t.Fatal("attempted without an expiry")
	}
	eq(t, c.count(), 0)
}

// No codex found, or a nil fallback: nothing runs.
func TestFallbackWithoutCLI(t *testing.T) {
	clk := clock.NewFake(t0)
	f := &CLIFinder{List: func() []string { return nil }, Clock: clk}
	s := &AppServer{CLIs: f, Clock: clk}
	f.CLI()
	a := authUntil(t0.Add(time.Hour))
	eq(t, s.Ready(a), false)
	if _, ok := s.Fetch(context.Background(), a); ok {
		t.Fatal("attempted without a CLI")
	}
	var none *AppServer
	if _, ok := none.Fetch(context.Background(), a); ok || none.Ready(a) {
		t.Fatal("nil fallback")
	}
}

// The three lines codex gets are the design's text (6.3), nothing else;
// it runs as `app-server --listen stdio://` with CODEX_HOME set.
func TestAppServerMessages(t *testing.T) {
	clk := clock.NewFake(t0)
	rec := filepath.Join(t.TempDir(), "rec.txt")
	s, _ := appServer(t, clk, fakeCodex(t, "-record", rec))
	s.Environ = []string{"PATH=" + os.Getenv("PATH"), "CODEX_HOME=/elsewhere", "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	ctx, cancel := budget(5 * time.Second)
	defer cancel()
	res, ok := s.Fetch(ctx, authUntil(t0.Add(time.Hour)))
	eq(t, ok, true)
	eq(t, res.HTTPStatus, 200)
	b, err := os.ReadFile(rec)
	must(t, err)
	var in []string
	head := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(l, "IN "):
			in = append(in, strings.TrimPrefix(l, "IN "))
		case strings.HasPrefix(l, "ARGS "), strings.HasPrefix(l, "CODEX_HOME "):
			k, v, _ := strings.Cut(l, " ")
			head[k] = v
		}
	}
	eq(t, head["ARGS"], "app-server --listen stdio://")
	eq(t, head["CODEX_HOME"], s.Home)
	eq(t, in, []string{
		`{"id":0,"method":"initialize","params":{"clientInfo":{"name":"ai_usage","title":"AI Usage","version":"0.0.0-test"}}}`,
		`{"method":"initialized"}`,
		`{"id":1,"method":"account/rateLimits/read","params":{"excludeResetCreditDetails":true}}`,
	})
	if !strings.Contains(string(b), "\nEOF") {
		t.Fatal("codex's input was not closed")
	}
}

// Each answer maps to the server's terms (design 6.3).
func TestAppServerErrorMapping(t *testing.T) {
	for _, c := range []struct {
		mode    string
		status  int
		problem string
		detail  string
	}{
		{"ok", 200, "", ""},
		{"autherr", 401, problemRPCError, "-32600 codex account authentication required to read rate limits"},
		{"ratelimit", 429, problemRPCError, "-32603 failed to fetch codex rate limits: 429 Too Many Requests"},
		{"nomethod", 0, ProblemCLIUpdate, "-32601 Method not found"},
		{"garbage", 0, problemEnded, ""},
		{"hang", 0, problemTimeout, ""},
	} {
		t.Run(c.mode, func(t *testing.T) {
			clk := clock.NewFake(t0)
			s, _ := appServer(t, clk, fakeCodex(t, "-mode", c.mode))
			ctx, cancel := budget(3 * time.Second)
			defer cancel()
			res, ok := s.Fetch(ctx, authUntil(t0.Add(time.Hour)))
			eq(t, ok, true)
			eq(t, res.HTTPStatus, c.status)
			eq(t, res.Source, "app-server")
			eq(t, res.Problem, c.problem)
			eq(t, res.Detail, c.detail)
			eq(t, res.CLIVersion, "9.9.9")
			if c.status == 200 {
				p := Parse(res.Usage)
				eq(t, p.MainKey, "codex:weekly")
				eq(t, len(p.Rows), 2)
			} else if res.Usage != nil {
				t.Fatalf("usage on %d", c.status)
			}
		})
	}
}

// "method not found": that executable is not used again until it
// changes, and the status says why.
func TestMethodNotFoundStopsThatCLI(t *testing.T) {
	clk := clock.NewFake(t0)
	cli := fakeCodex(t, "-mode", "nomethod")
	s, c := appServer(t, clk, cli)
	ctx, cancel := budget(3 * time.Second)
	defer cancel()
	a := authUntil(t0.Add(5 * time.Hour))
	if _, ok := s.Fetch(ctx, a); !ok {
		t.Fatal("not attempted")
	}
	_, problem := s.Status()
	eq(t, problem, ProblemCLIUpdate)
	clk.Advance(StartGap)
	eq(t, s.Ready(a), false)
	if _, ok := s.Fetch(ctx, a); ok {
		t.Fatal("ran the old CLI again")
	}
	eq(t, c.count(), 1)
	// an update: the file changes
	must(t, os.WriteFile(cli+".args", []byte("-mode\nok\n"), 0o600))
	later := time.Now().Add(time.Minute)
	must(t, os.Chtimes(cli, later, later))
	clk.Advance(10 * time.Minute)
	cliText, problem := s.Status()
	eq(t, problem, "")
	eq(t, cliText, cli+" 9.9.9")
	res, ok := s.Fetch(ctx, a)
	eq(t, ok, true)
	eq(t, res.HTTPStatus, 200)
}

// A codex that never answers and never ends, with a child of its own that
// sleeps, is ended (with its child) inside the caller's deadline, and the
// run leaves no goroutine behind.
func TestAppServerHangIsKilled(t *testing.T) {
	for _, ignoreTerm := range []bool{false, true} {
		clk := clock.NewFake(t0)
		pidfile := filepath.Join(t.TempDir(), "pids")
		flags := []string{"-mode", "child", "-pidfile", pidfile}
		if ignoreTerm {
			flags = append(flags, "-ignore-term")
		}
		s, _ := appServer(t, clk, fakeCodex(t, flags...))
		const total = 2500 * time.Millisecond
		ctx, cancel := budget(total)
		start := time.Now()
		res, ok := s.Fetch(ctx, authUntil(t0.Add(time.Hour)))
		took := time.Since(start)
		cancel()
		eq(t, ok, true)
		eq(t, res.HTTPStatus, 0)
		eq(t, res.Problem, problemTimeout)
		if took > total {
			t.Fatalf("ignore TERM %v: took %v, the budget is %v", ignoreTerm, took, total)
		}
		pids := pidsIn(t, pidfile)
		eq(t, len(pids), 2)
		noneAlive(t, pids)
		noRunGoroutines(t)
	}
}

// A codex that answers but does not end when its input closes is killed
// after the exit grace.
func TestNoExitAfterStdinClose(t *testing.T) {
	clk := clock.NewFake(t0)
	pidfile := filepath.Join(t.TempDir(), "pids")
	s, _ := appServer(t, clk, fakeCodex(t, "-mode", "noexit", "-pidfile", pidfile))
	ctx, cancel := budget(10 * time.Second)
	defer cancel()
	start := time.Now()
	res, ok := s.Fetch(ctx, authUntil(t0.Add(time.Hour)))
	took := time.Since(start)
	eq(t, ok, true)
	eq(t, res.HTTPStatus, 200)
	if took < s.ExitGrace || took > s.ExitGrace+2*time.Second {
		t.Fatalf("took %v (exit grace %v)", took, s.ExitGrace)
	}
	noneAlive(t, pidsIn(t, pidfile))
	noRunGoroutines(t)
}

// With less of the caller's deadline left than the requests need, codex
// is not started at all.
func TestShortDeadlineStartsNothing(t *testing.T) {
	clk := clock.NewFake(t0)
	s, c := appServer(t, clk, fakeCodex(t))
	s.MinCall, s.ExitGrace, s.ReapMargin = 0, 0, 0 // the real budget
	ctx, cancel := budget(MinCall + ExitGrace + ReapMargin - time.Second)
	defer cancel()
	if _, ok := s.Fetch(ctx, authUntil(t0.Add(time.Hour))); ok {
		t.Fatal("attempted without the time it needs")
	}
	eq(t, c.count(), 0)
}

// At most one start per StartGap.
func TestOneStartPerGap(t *testing.T) {
	clk := clock.NewFake(t0)
	s, c := appServer(t, clk, fakeCodex(t))
	a := authUntil(t0.Add(5 * time.Hour))
	ctx, cancel := budget(5 * time.Second)
	defer cancel()
	if _, ok := s.Fetch(ctx, a); !ok {
		t.Fatal("first")
	}
	clk.Advance(StartGap - time.Second)
	eq(t, s.Ready(a), false)
	if _, ok := s.Fetch(ctx, a); ok {
		t.Fatal("second start inside the gap")
	}
	clk.Advance(time.Second)
	eq(t, s.Ready(a), true)
	if _, ok := s.Fetch(ctx, a); !ok {
		t.Fatal("after the gap")
	}
	eq(t, c.count(), 2)
}

// Versions are asked only for new or changed executables, and the list
// at most every 10 minutes; the highest version wins.
func TestCLIFinderAsksVersionsOnlyWhenChanged(t *testing.T) {
	clk := clock.NewFake(t0)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	must(t, os.WriteFile(a, []byte("x"), 0o755))
	must(t, os.WriteFile(b, []byte("y"), 0o755))
	asked := map[string]int{}
	versions := map[string]string{a: "codex-cli 0.160.1", b: "codex-cli 0.172.0"}
	f := &CLIFinder{List: func() []string { return []string{a, b} }, Clock: clk,
		Version: func(p string) (string, error) { asked[p]++; return versions[p], nil }}
	cli, ok := f.CLI()
	eq(t, ok, true)
	eq(t, cli, CLI{Path: b, Version: "0.172.0"})
	clk.Advance(9 * time.Minute)
	f.CLI()
	clk.Advance(time.Minute)
	f.CLI()
	eq(t, asked, map[string]int{a: 1, b: 1})
	// a is updated
	versions[a] = "codex-cli 0.180.0"
	must(t, os.WriteFile(a, []byte("xx"), 0o755))
	clk.Advance(10 * time.Minute)
	cli, _ = f.CLI()
	eq(t, cli, CLI{Path: a, Version: "0.180.0"})
	eq(t, asked, map[string]int{a: 2, b: 1})
}

func TestRunVersion(t *testing.T) {
	out, err := RunVersion(fakeCodex(t))
	must(t, err)
	v, ok := ParseVersion(out)
	eq(t, ok, true)
	eq(t, v, "9.9.9")
	if _, err := RunVersion(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ran a missing file")
	}
}

// Ruling F1: unless AllowCLIRuns(true), nothing in this package starts a
// codex executable: not --version (directly or through CLIFinder), not
// app-server (Ready says no, Fetch does not try, even with a CLI known
// from before), and Start is never called. The fakecodex leaves a record
// whenever it runs.
func TestNoCodexRunsUnlessAllowed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	cli := fakeCodex(t, "-record", marker)
	clk := clock.NewFake(time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC))
	s, c := appServer(t, clk, cli) // CLIFinder looked once while allowed (TestMain)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the fakecodex left no record while allowed: %v", err)
	}
	must(t, os.Remove(marker))

	AllowCLIRuns(false)
	t.Cleanup(func() { AllowCLIRuns(true) })
	if _, err := RunVersion(cli); err != ErrCLIRunsOff {
		t.Fatalf("RunVersion: %v", err)
	}
	f := &CLIFinder{List: func() []string { return []string{cli} }, Clock: clk}
	if got, ok := f.CLI(); ok {
		t.Fatalf("CLIFinder found %+v without running anything?", got)
	}
	a := authUntil(clk.Now().Add(9 * 24 * time.Hour))
	eq(t, s.Ready(a), false)
	ctx, cancel := budget(5 * time.Second)
	defer cancel()
	if res, attempted := s.Fetch(ctx, a); attempted {
		t.Fatalf("Fetch attempted: %+v", res)
	}
	r := &appRun{s: s, cli: CLI{Path: cli, Version: "9.9.9"}, deadline: time.Now().Add(5 * time.Second)}
	var res provider.Result
	r.run(ctx, &res)
	eq(t, res.Problem, problemStart)
	eq(t, c.count(), 0)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a codex was run: %v", err)
	}
}

// A codex error message reaches the server without e-mail addresses or
// long ids, at most 200 bytes.
func TestDetailIsCleaned(t *testing.T) {
	d := clipDetail("-32603 " + cleanDetail("account user-x@example.com id 0f3c2a9b8e7d6c5b4a39281706f5e4d3 failed "+strings.Repeat("é", 200)))
	if strings.Contains(d, "example.com") || strings.Contains(d, "0f3c2a9b") || len(d) > 200 {
		t.Fatalf("%q", d)
	}
	if !strings.HasPrefix(d, "-32603 account <email> id <id> failed") {
		t.Fatalf("%q", d)
	}
}

// noRunGoroutines fails if a goroutine of a fallback run still exists.
func noRunGoroutines(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(time.Second); ; {
		buf := make([]byte, 1<<20)
		st := string(buf[:runtime.Stack(buf, true)])
		if !strings.Contains(st, "codex.(*appRun)") {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("a run's goroutine is left:\n%s", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
