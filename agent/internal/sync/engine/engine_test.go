package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

const (
	ingestKey = "test-ingest-key-0123456789"
	usageBody = `{"seven_day":{"utilization":5,"resets_at":"2026-01-09T06:00:00+00:00"},"limits":[{"kind":"weekly_all","percent":5,"severity":"normal","resets_at":"2026-01-09T06:00:00+00:00"}]}`
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

type fakeLogins struct{ login creds.Login }

func (f *fakeLogins) Current(context.Context) creds.Login { return f.login }

type fakeFetcher struct {
	clk     clock.Clock
	calls   int
	status  int
	at      []time.Time
	body    string
	onFetch func()
}

func (f *fakeFetcher) Fetch(context.Context) (anthropic.Result, error) {
	f.calls++
	now := f.clk.Now()
	f.at = append(f.at, now)
	if f.onFetch != nil {
		f.onFetch()
	}
	st := f.status
	if st == 0 {
		st = 200
	}
	r := anthropic.Result{FetchedAt: now.UTC().Truncate(time.Millisecond), HTTPStatus: st}
	if st == 200 {
		r.Usage = json.RawMessage(usageBody)
		if f.body != "" {
			r.Usage = json.RawMessage(f.body)
		}
	}
	return r, nil
}

type call struct {
	base string
	req  protocol.Request
}

type fakeSyncer struct {
	mu    sync.Mutex
	calls []call
	fn    func(n int, base string, req protocol.Request) (protocol.Response, error)
}

func (f *fakeSyncer) Sync(_ context.Context, base string, req protocol.Request) (protocol.Response, error) {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, call{base, req})
	f.mu.Unlock()
	return f.fn(n, base, req)
}

func (f *fakeSyncer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSyncer) reqs() []protocol.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []protocol.Request
	for _, c := range f.calls {
		out = append(out, c.req)
	}
	return out
}

func resp(role string, fetchNow bool, next int) protocol.Response {
	return protocol.Response{Role: role, FetchNow: fetchNow, NextSyncSeconds: next, Peers: protocol.Peers{Devices: 1}}
}

func always(r protocol.Response, err error) func(int, string, protocol.Request) (protocol.Response, error) {
	return func(int, string, protocol.Request) (protocol.Response, error) { return r, err }
}

var errDown = errors.New("sync: connection refused")

func okLogin() creds.Login {
	return creds.Login{HasToken: true, ExpiresAt: t0.Add(8 * time.Hour), SubscriptionType: "max", RateLimitTier: "default_claude_max_20x",
		Account: &creds.Account{UUID: "acc-1", Email: "user-a@example.com", DisplayName: "User A", OrganizationName: "Example", OrganizationType: "claude_max"},
		Source:  "file"}
}

type rig struct {
	e       *Engine
	clk     *clock.Fake
	logins  *fakeLogins
	fetcher *fakeFetcher
	syncer  *fakeSyncer
	backlog *backlog.Store
	dir     string
	snaps   []status.Snapshot
	rnd     []float64
	ri      int
}

func newRig(t *testing.T, fn func(int, string, protocol.Request) (protocol.Response, error)) *rig {
	t.Helper()
	r := &rig{clk: clock.NewFake(t0), logins: &fakeLogins{login: okLogin()}, syncer: &fakeSyncer{fn: fn}, dir: t.TempDir()}
	r.fetcher = &fakeFetcher{clk: r.clk}
	bl, err := backlog.Open(filepath.Join(r.dir, "backlog.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.backlog = bl
	r.e = r.build()
	return r
}

func (r *rig) build() *Engine {
	return New(Deps{
		Clock: r.clk, Logins: r.logins, Fetcher: r.fetcher, Syncer: r.syncer, Backlog: r.backlog,
		Config:    config.Config{ServerURLs: []string{"http://192.0.2.10:8700"}, IngestKey: ingestKey},
		StatePath: filepath.Join(r.dir, "state.json"), LatestPath: filepath.Join(r.dir, "latest.json"),
		Identity: Identity{DeviceID: "dev-a", Hostname: "TEST-PC-A", OSUser: "user-a", OS: "linux", OSVersion: "24.04",
			Arch: "amd64", Version: "0.0.0-dev", Mode: "headless"},
		Publish: func(s status.Snapshot) { r.snaps = append(r.snaps, s) },
		Rand: func() float64 {
			if len(r.rnd) == 0 {
				return 0.5
			}
			v := r.rnd[r.ri%len(r.rnd)]
			r.ri++
			return v
		},
	})
}

func (r *rig) step() time.Duration {
	d := r.e.Step(ctx)
	r.clk.Advance(d)
	return d
}

func (r *rig) snap() status.Snapshot { return r.snaps[len(r.snaps)-1] }

// stepUntil steps until cond holds, failing after limit of fake time.
func (r *rig) stepUntil(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	start := r.clk.Now()
	for !cond() {
		if r.clk.Now().Sub(start) > limit {
			t.Fatalf("condition not reached within %v of fake time", limit)
		}
		r.step()
	}
}

func TestPollerFetchesAndSyncsAtOnce(t *testing.T) {
	r := newRig(t, func(n int, _ string, _ protocol.Request) (protocol.Response, error) {
		if n == 0 {
			return resp("poller", true, 60), nil
		}
		return resp("poller", false, 60), nil
	})
	d := r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
	reqs := r.syncer.reqs()
	eq(t, len(reqs), 2)
	eq(t, len(reqs[0].Results), 0)
	eq(t, len(reqs[1].Results), 1)
	eq(t, reqs[1].Results[0].HTTPStatus, 200)
	eq(t, reqs[1].Results[0].AccountUUID, "acc-1")
	eq(t, r.backlog.Len(), 0)
	eq(t, d, 60*time.Second)
	eq(t, r.snap().Mode, "coordinated")
	eq(t, r.snap().Role, "poller")
	eq(t, *r.snap().WeeklyPercent, 5.0)
}

func TestSecondFetchNowInSameStepIsIgnored(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 60), nil))
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
	eq(t, len(r.syncer.calls), 2)
}

func TestStandbyNeverFetches(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	for i := 0; i < 60; i++ {
		r.step()
	}
	eq(t, r.fetcher.calls, 0)
	eq(t, len(r.syncer.calls), 60)
	// a standby is never told to fetch; if a server said so anyway, it still does not
	r.syncer.fn = always(resp("standby", true, 60), nil)
	r.step()
	eq(t, r.fetcher.calls, 0)
}

func TestRequestFields(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	r.e.Step(ctx)
	req := r.syncer.reqs()[0]
	eq(t, req.Schema, 1)
	eq(t, req.Client, protocol.ClientInfo{Version: "0.0.0-dev", OS: "linux", OSVersion: "24.04", Arch: "amd64", Mode: "headless"})
	eq(t, req.Device, protocol.DeviceInfo{ID: "dev-a", Hostname: "TEST-PC-A", OSUser: "user-a"})
	eq(t, req.Account.UUID, "acc-1")
	eq(t, req.Account.Email, protocol.Opt("user-a@example.com"))
	eq(t, req.Account.SubscriptionType, protocol.Opt("max"))
	eq(t, req.Account.RateLimitTier, protocol.Opt("default_claude_max_20x"))
	eq(t, req.Auth.State, "ok")
	eq(t, req.Auth.TokenExpiresAt.Time, t0.Add(8*time.Hour))
	eq(t, req.WantFresh, false)
	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), `"token_expires_at":"2026-01-05T17:00:00.000Z"`) || !strings.Contains(string(b), `"results":[]`) {
		t.Fatal(string(b))
	}

	r.logins.login = creds.Login{Problem: "no Claude Code sign-in found"}
	r.clk.Advance(time.Minute)
	r.e.Step(ctx)
	req = r.syncer.reqs()[1]
	eq(t, req.Account == nil, true)
	eq(t, req.Auth.State, "not_signed_in")
	eq(t, req.Auth.TokenExpiresAt == nil, true)
	b, _ = json.Marshal(req)
	if !strings.Contains(string(b), `"account":null`) || !strings.Contains(string(b), `"results":[]`) {
		t.Fatal(string(b))
	}
	eq(t, r.snap().AuthState, "not_signed_in")
}

func TestLongTextsAreClipped(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	l := okLogin()
	l.Account.DisplayName = strings.Repeat("é", 300)
	r.logins.login = l
	r.e.Step(ctx)
	eq(t, len([]rune(string(r.syncer.reqs()[0].Account.DisplayName))), 256)
}

func TestThreeFailuresEnterStandalone(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, errDown))
	for i := 0; i < 2; i++ {
		eq(t, r.step(), 60*time.Second)
		eq(t, r.snap().Mode, "starting")
	}
	entered := r.clk.Now()
	r.step()
	eq(t, r.snap().Mode, "standalone")
	eq(t, r.snap().SyncFailures, 3)
	eq(t, r.fetcher.calls, 0)
	r.stepUntil(t, time.Hour, func() bool { return r.fetcher.calls >= 2 })
	// first standalone fetch at +2.5 min after entering (Rand 0.5), the next 30 min after that
	eq(t, r.fetcher.at[0], entered.Add(150*time.Second))
	eq(t, r.fetcher.at[1], entered.Add(150*time.Second+30*time.Minute))
	eq(t, r.backlog.Len(), 2)
	eq(t, r.snap().Backlog, 2)
	// it keeps looking for the server every 60 s at most
	n := len(r.syncer.calls)
	r.step()
	if len(r.syncer.calls) == n {
		t.Fatal("no sync attempt")
	}
}

func TestStandaloneIntervalStaysIn25to35(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, errDown))
	r.rnd = []float64{0, 0.999, 0.3, 0.7, 0.05, 0.95}
	for r.clk.Now().Sub(t0) < 6*time.Hour {
		d := r.step()
		if d <= 0 || d > 60*time.Second {
			t.Fatalf("wait %v", d)
		}
	}
	if r.fetcher.calls < 9 {
		t.Fatalf("only %d fetches in 6 hours", r.fetcher.calls)
	}
	sawMin := false
	for i := 1; i < len(r.fetcher.at); i++ {
		gap := r.fetcher.at[i].Sub(r.fetcher.at[i-1])
		if gap < 25*time.Minute || gap >= 35*time.Minute {
			t.Fatalf("gap %d: %v", i, gap)
		}
		sawMin = sawMin || gap == 25*time.Minute
	}
	if !sawMin {
		t.Fatal("Rand 0 should give exactly 25 min")
	}
}

func TestStandaloneWaitsForRateLimit(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, errDown))
	r.fetcher.status = 429
	r.stepUntil(t, time.Hour, func() bool { return r.fetcher.calls >= 1 })
	// after a 429 the limiter allows the next fetch after 120 s, which is
	// shorter than 25 min: the standalone interval still applies
	r.stepUntil(t, time.Hour, func() bool { return r.fetcher.calls >= 2 })
	gap := r.fetcher.at[1].Sub(r.fetcher.at[0])
	if gap < 25*time.Minute {
		t.Fatalf("gap %v", gap)
	}
}

func TestStandaloneSkipsWithoutLogin(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, errDown))
	l := okLogin()
	l.ExpiresAt = t0.Add(-time.Minute)
	r.logins.login = l
	start := r.clk.Now()
	steps := 0
	for r.clk.Now().Sub(start) < 2*time.Hour {
		eq(t, r.step(), 60*time.Second) // never faster while signed out
		steps++
	}
	eq(t, steps, 120)
	eq(t, r.fetcher.calls, 0)
	eq(t, r.snap().Mode, "standalone")
	eq(t, r.snap().AuthState, "expired")
}

func TestOutageThenRecoveryDeliversBacklog(t *testing.T) {
	down := true
	r := newRig(t, func(int, string, protocol.Request) (protocol.Response, error) {
		if down {
			return protocol.Response{}, errDown
		}
		return resp("standby", false, 60), nil
	})
	for i := 0; i < 3; i++ {
		r.step()
	}
	eq(t, r.snap().Mode, "standalone")
	start := r.clk.Now()
	for r.clk.Now().Sub(start) < 4*time.Hour {
		r.step()
	}
	// continue until a step that fetched, so the recovery step is not a fetch step
	n0 := r.fetcher.calls
	r.stepUntil(t, time.Hour, func() bool { return r.fetcher.calls > n0 })
	fetched := r.fetcher.calls
	if fetched < 7 || fetched > 10 {
		t.Fatalf("%d standalone fetches in 4 hours", fetched)
	}
	eq(t, r.backlog.Len(), fetched)

	// the agent restarts in the middle: the backlog is still there
	bl, err := backlog.Open(filepath.Join(r.dir, "backlog.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.backlog = bl
	r.e = r.build()
	eq(t, r.backlog.Len(), fetched)

	down = false
	n := len(r.syncer.calls)
	r.step()
	req := r.syncer.calls[n].req
	eq(t, len(req.Results), fetched)
	for i := 1; i < len(req.Results); i++ {
		if !req.Results[i-1].FetchedAt.Before(req.Results[i].FetchedAt.Time) {
			t.Fatal("not oldest first")
		}
	}
	eq(t, r.backlog.Len(), 0)
	eq(t, r.snap().Mode, "coordinated")
	eq(t, r.snap().Backlog, 0)
	eq(t, r.fetcher.calls, fetched) // the recovery step did not fetch
}

func TestTooLargeHalvesBatch(t *testing.T) {
	var sizes []int
	tooLarge := true
	r := newRig(t, func(_ int, _ string, req protocol.Request) (protocol.Response, error) {
		sizes = append(sizes, len(req.Results))
		if tooLarge && len(req.Results) > 500 {
			return protocol.Response{}, &protocol.StatusError{Status: 413}
		}
		return resp("standby", false, 60), nil
	})
	add := func(n int, from time.Time) {
		for i := 0; i < n; i++ {
			_ = r.backlog.Add(protocol.Result{AccountUUID: "acc-1", FetchedAt: protocol.Time{Time: from.Add(time.Duration(i) * time.Second)}, HTTPStatus: 200})
		}
	}
	add(1000, t0.Add(-24*time.Hour))
	r.step()
	eq(t, sizes[0], 1000)
	eq(t, r.snap().SyncFailures, 1)
	eq(t, config.LoadState(filepath.Join(r.dir, "state.json")).BatchLimit, 500)
	n := len(sizes)
	// the halved limit survives a restart (state.json)
	r.e = r.build()
	r.step()
	eq(t, sizes[n], 500)
	eq(t, r.backlog.Len(), 500)
	eq(t, config.LoadState(filepath.Join(r.dir, "state.json")).BatchLimit, 0) // back to the default
	tooLarge = false
	add(600, r.clk.Now().Add(-time.Hour)) // 1100 pending, pruned to the newest 1000
	r.step()
	eq(t, sizes[len(sizes)-1], 1000)
	eq(t, r.backlog.Len(), 0)
}

func TestDiscoveryOrderAndLastGood(t *testing.T) {
	r := newRig(t, func(_ int, base string, _ protocol.Request) (protocol.Response, error) {
		if base == "http://ai-usage.local" {
			return protocol.Response{}, errDown
		}
		return resp("standby", false, 60), nil
	})
	r.step()
	eq(t, r.syncer.calls[0].base, "http://ai-usage.local")
	eq(t, r.syncer.calls[1].base, "http://ai-usage.local:8700")
	eq(t, config.LoadState(filepath.Join(r.dir, "state.json")).LastGoodURL, "http://ai-usage.local:8700")
	eq(t, r.snap().ServerURL, "http://ai-usage.local:8700")
	r.step()
	eq(t, r.syncer.calls[2].base, "http://ai-usage.local:8700")
	eq(t, len(r.syncer.calls), 3)
}

func TestAnnouncedURLsAreUsed(t *testing.T) {
	up := true
	r := newRig(t, func(_ int, base string, _ protocol.Request) (protocol.Response, error) {
		if up {
			x := resp("standby", false, 60)
			x.ServerURLs = []string{"http://192.0.2.20:8700"}
			return x, nil
		}
		return protocol.Response{}, errDown
	})
	r.step()
	eq(t, config.LoadState(filepath.Join(r.dir, "state.json")).ServerURLs, []string{"http://192.0.2.20:8700"})
	up = false
	n := len(r.syncer.calls)
	r.e = r.build() // also after a restart
	r.step()
	var bases []string
	for _, c := range r.syncer.calls[n:] {
		bases = append(bases, c.base)
	}
	eq(t, bases, []string{"http://ai-usage.local", "http://ai-usage.local:8700", "http://192.0.2.20:8700", "http://192.0.2.10:8700"})
}

func TestBadIngestKeyCountsAsFailure(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, protocol.ErrBadIngestKey))
	for i := 0; i < 3; i++ {
		r.step()
	}
	eq(t, r.snap().Mode, "standalone")
	eq(t, r.snap().SyncProblem, "server rejected the ingest key")
	eq(t, r.snap().LastSyncOK, false)
}

func TestServerErrorIsPreferredInProblem(t *testing.T) {
	r := newRig(t, func(_ int, base string, _ protocol.Request) (protocol.Response, error) {
		if base == "http://ai-usage.local" {
			return protocol.Response{}, protocol.ErrBadIngestKey
		}
		return protocol.Response{}, errDown
	})
	r.step()
	eq(t, r.snap().SyncProblem, "server rejected the ingest key")
}

func TestLimiterBlocksTooSoonFetch(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 5), nil))
	eq(t, r.e.Step(ctx), 15*time.Second) // clamped
	eq(t, r.fetcher.calls, 1)
	// 5 s later the server says fetch_now again: too soon
	r.clk.Advance(5 * time.Second)
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
	r.clk.Advance(54 * time.Second) // 59 s after the fetch: still too soon
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
	r.clk.Advance(time.Second)
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 2)
	// refused orders are not sync failures
	eq(t, r.snap().SyncFailures, 0)
	eq(t, r.snap().Mode, "coordinated")
}

// fetchesInAnyHour is the largest number of fetches in a rolling hour.
func fetchesInAnyHour(at []time.Time) int {
	most := 0
	for i := range at {
		n := 0
		for j := i; j < len(at) && at[j].Sub(at[i]) < time.Hour; j++ {
			n++
		}
		most = max(most, n)
	}
	return most
}

func TestServerOrderingFetchOnEverySyncIsCapped(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 0), nil))
	for r.clk.Now().Sub(t0) < 3*time.Hour {
		r.step()
	}
	if n := fetchesInAnyHour(r.fetcher.at); n > 15 {
		t.Fatalf("%d fetches in one hour", n)
	}
	for i := 1; i < len(r.fetcher.at); i++ {
		if gap := r.fetcher.at[i].Sub(r.fetcher.at[i-1]); gap < time.Minute {
			t.Fatalf("gap %v", gap)
		}
	}
	eq(t, len(r.fetcher.at), 45) // the budget is used, never exceeded
	eq(t, r.snap().SyncFailures, 0)
	eq(t, r.snap().Mode, "coordinated")
}

func TestFetchLimitSurvivesRestart(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 0), nil))
	for r.clk.Now().Sub(t0) < 20*time.Minute {
		r.step()
	}
	eq(t, r.fetcher.calls, 15)
	// a crash-restart loop must not reset the budget
	for r.clk.Now().Sub(t0) < 59*time.Minute {
		r.e = r.build()
		r.step()
	}
	eq(t, r.fetcher.calls, 15)
	r.clk.Set(t0.Add(time.Hour))
	r.e = r.build()
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 16)
	// and the 60 s gap holds across a restart as well
	r.e = r.build()
	r.clk.Advance(30 * time.Second)
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 16)
}

func TestStandaloneCountsTowardTheSameBudget(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 0), nil))
	for r.clk.Now().Sub(t0) < 20*time.Minute {
		r.step()
	}
	eq(t, r.fetcher.calls, 15)
	r.syncer.fn = always(protocol.Response{}, errDown)
	r.stepUntil(t, time.Hour, func() bool { return r.snap().Mode == "standalone" })
	for r.clk.Now().Sub(t0) < 59*time.Minute {
		r.step()
	}
	eq(t, r.fetcher.calls, 15) // the stand-alone fetch waits for the hourly budget
	r.stepUntil(t, 10*time.Minute, func() bool { return r.fetcher.calls == 16 })
	if r.fetcher.at[15].Before(r.fetcher.at[0].Add(time.Hour)) {
		t.Fatal("stand-alone fetch broke the hourly budget")
	}
}

func TestFailureCountResetsAfterSuccess(t *testing.T) {
	up := false
	r := newRig(t, func(int, string, protocol.Request) (protocol.Response, error) {
		if up {
			return resp("standby", false, 60), nil
		}
		return protocol.Response{}, errDown
	})
	for i := 0; i < 3; i++ {
		r.step()
	}
	eq(t, r.snap().Mode, "standalone")
	up = true
	r.step()
	eq(t, r.snap().Mode, "coordinated")
	eq(t, r.snap().SyncFailures, 0)
	up = false
	r.step() // one lost sync after recovery
	eq(t, r.snap().Mode, "coordinated")
	eq(t, r.snap().SyncFailures, 1)
	r.step()
	r.step()
	eq(t, r.snap().Mode, "standalone")
}

func TestFailedSyncAfterFetchCounts(t *testing.T) {
	// the first sync succeeds and orders a fetch; every call after that fails,
	// including the sync right after the fetch
	first := true
	r := newRig(t, func(int, string, protocol.Request) (protocol.Response, error) {
		if first {
			first = false
			return resp("poller", true, 60), nil
		}
		return protocol.Response{}, errDown
	})
	d := r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
	eq(t, r.snap().SyncFailures, 1)
	eq(t, d, RetryInterval)
	eq(t, r.backlog.Len(), 1) // kept for the next sync
	eq(t, r.snap().Mode, "coordinated")
	// it counts toward the three: two more failed steps and the agent is stand-alone
	r.clk.Advance(d)
	r.step()
	eq(t, r.snap().Mode, "coordinated")
	r.step()
	eq(t, r.snap().Mode, "standalone")
}

func TestNextSyncClamped(t *testing.T) {
	for in, want := range map[int]time.Duration{1: 15 * time.Second, 0: 15 * time.Second, 60: 60 * time.Second, 99999: 600 * time.Second} {
		r := newRig(t, always(resp("standby", false, in), nil))
		eq(t, r.e.Step(ctx), want)
	}
}

func TestLatestIsShownAndSaved(t *testing.T) {
	x := resp("standby", false, 60)
	poller := "TEST-PC-B"
	x.Peers = protocol.Peers{Devices: 2, PollerHostname: &poller}
	x.Latest = &protocol.Latest{FetchedAt: "2026-01-05T08:59:00.000Z", Usage: json.RawMessage(usageBody)}
	r := newRig(t, always(x, nil))
	r.step()
	s := r.snap()
	eq(t, *s.WeeklyPercent, 5.0)
	eq(t, s.LatestFetchedAt, time.Date(2026, 1, 5, 8, 59, 0, 0, time.UTC))
	eq(t, s.PeersDevices, 2)
	eq(t, s.PollerHostname, "TEST-PC-B")
	eq(t, s.WeeklyResetsAt, time.Date(2026, 1, 9, 6, 0, 0, 0, time.UTC))
	var saved protocol.Latest
	b, err := os.ReadFile(filepath.Join(r.dir, "latest.json"))
	if err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatalf("latest.json: %v", err)
	}
	eq(t, saved.FetchedAt, "2026-01-05T08:59:00.000Z")
}

func TestRunStopsOnCancel(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { Run(c, r.e, r.clk); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestStatusHasNoSecrets(t *testing.T) {
	r := newRig(t, func(n int, _ string, _ protocol.Request) (protocol.Response, error) {
		if n%2 == 0 {
			return resp("poller", true, 60), nil
		}
		return protocol.Response{}, fmt.Errorf("sync: HTTP 500")
	})
	for i := 0; i < 10; i++ {
		r.step()
	}
	b, _ := json.Marshal(r.snaps)
	if strings.Contains(string(b), ingestKey) || strings.Contains(string(b), "fake-access-token") {
		t.Fatal(string(b))
	}
}

func TestNoFetchWithoutAccount(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 60), nil))
	l := okLogin()
	l.Account = nil // token present, .claude.json missing: a result would be dropped by the server
	r.logins.login = l
	r.step()
	eq(t, r.fetcher.calls, 0)
	r.syncer.fn = always(protocol.Response{}, errDown)
	for r.clk.Now().Sub(t0) < time.Hour {
		r.step()
	}
	eq(t, r.snap().Mode, "standalone")
	eq(t, r.fetcher.calls, 0)
}

func TestEmptyUsageDoesNotReplaceNumbers(t *testing.T) {
	x := resp("poller", true, 60)
	x.Latest = &protocol.Latest{FetchedAt: "2026-01-05T08:59:00.000Z", Usage: json.RawMessage(usageBody)}
	r := newRig(t, always(x, nil))
	r.fetcher.body = `{}`
	r.step()
	eq(t, r.fetcher.calls, 1)
	eq(t, *r.snap().WeeklyPercent, 5.0)
	eq(t, len(r.syncer.calls[1].req.Results), 1) // the result still goes to the server unchanged
	eq(t, string(r.syncer.calls[1].req.Results[0].Usage), `{}`)
}

func TestShutdownDuringFetchDropsResult(t *testing.T) {
	r := newRig(t, always(resp("poller", true, 60), nil))
	c, cancel := context.WithCancel(ctx)
	r.fetcher.onFetch = cancel // SIGTERM arrives while the request is in flight
	r.e.Step(c)
	eq(t, r.fetcher.calls, 1)
	eq(t, r.backlog.Len(), 0)
	// the attempt still counts locally, also after a restart
	r.e = r.build()
	r.fetcher.onFetch = nil
	r.clk.Advance(30 * time.Second)
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
}

func TestSignInProblemIsShown(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	l := okLogin()
	l.Account, l.Problem = nil, "account file larger than 32 MiB"
	r.logins.login = l
	r.step()
	eq(t, r.snap().SignInProblem, "account file larger than 32 MiB")
	r.logins.login = okLogin()
	r.step()
	eq(t, r.snap().SignInProblem, "")
}
