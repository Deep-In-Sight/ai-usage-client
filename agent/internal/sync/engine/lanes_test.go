package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// fakeProvider is a second provider for lane tests: always signed in, its
// replies are {"p": <percent>} with one row.
type fakeProvider struct {
	id     provider.ID
	clk    clock.Clock
	status int
	state  string
	at     []time.Time
	wait   time.Duration // Retry-After of a 429 (0: 90 s)
}

func (f *fakeProvider) ID() provider.ID { return f.id }

func (f *fakeProvider) Login(context.Context) provider.Login {
	st := f.state
	if st == "" {
		st = "ok"
	}
	return provider.Login{Provider: f.id, State: st, AccountKey: string(f.id) + ":acc:user", Email: "user-a@example.com", Plan: "plus"}
}

func (f *fakeProvider) Fetch(context.Context) (provider.Result, error) {
	now := f.clk.Now()
	f.at = append(f.at, now)
	st := f.status
	if st == 0 {
		st = 200
	}
	r := provider.Result{Provider: f.id, FetchedAt: now.UTC().Truncate(time.Millisecond), HTTPStatus: st, Source: "wham"}
	if st == 200 {
		r.Usage = json.RawMessage(`{"p":17}`)
	}
	if st == 429 {
		r.RetryAfter = 90 * time.Second
		if f.wait != 0 {
			r.RetryAfter = f.wait
		}
	}
	return r, nil
}

func (f *fakeProvider) Parse(raw json.RawMessage) provider.Parsed {
	var v struct{ P *float64 }
	_ = json.Unmarshal(raw, &v)
	return provider.Parsed{Rows: []provider.Row{{Key: "main", Label: "Weekly", Percent: v.P, Level: usage.LevelNormal}}, MainKey: "main"}
}

// Lanes are independent: one provider answering 429 again and again does
// not move another provider's fetches by a second.
func TestLanesAreIndependent(t *testing.T) {
	var dir string
	run := func(claude429 bool) (codex []time.Time, claudeCalls int, snap status.Snapshot, bl *backlog.Store) {
		r := newRig(t, always(protocol.Response{}, errDown))
		if claude429 {
			r.fetcher.status = 429
		} else {
			r.logins.login = creds.Login{} // no Claude sign-in: its lane never fetches
		}
		other := &fakeProvider{id: provider.Codex, clk: r.clk}
		r.extra = []provider.Provider{other}
		r.e = r.build()
		for r.clk.Now().Sub(t0) < 4*time.Hour {
			r.step()
		}
		dir = r.dir
		return other.at, r.fetcher.calls, r.snap(), r.backlog
	}
	alone, _, _, _ := run(false)
	codex, claudeCalls, snap, bl := run(true)
	if claudeCalls < 5 {
		t.Fatalf("the Claude lane fetched %d times", claudeCalls)
	}
	if len(alone) < 7 {
		t.Fatalf("only %d Codex fetches in 4 hours", len(alone))
	}
	eq(t, codex, alone)
	// each lane reports its own fetches
	eq(t, *snap.Providers[provider.Claude].LastFetchStatus, 429)
	eq(t, *snap.Providers[provider.Codex].LastFetchStatus, 200)
	eq(t, len(bl.Pending(5000, "codex")), len(codex))
	eq(t, len(bl.Pending(5000, "claude")), claudeCalls)
	if it := bl.Pending(1, "claude")[0]; it.RetryAfterSeconds != nil {
		t.Fatalf("the fake Anthropic reply had no Retry-After, item says %d", *it.RetryAfterSeconds)
	}
	// and each lane keeps its own budget in state.json
	st := config.LoadState(filepath.Join(dir, "state.json"))
	if st.FetchLimit.Consecutive429 == 0 || st.FetchLimits["codex"].Consecutive429 != 0 || len(st.FetchLimits["codex"].Attempts) == 0 {
		t.Fatalf("state %+v", st)
	}
}

// The files an installed v0.8.1 agent left (written by the v0.8.1 code)
// carry over: latest.json becomes latest-claude.json and is shown, the
// backlog items (no provider) go to the server as Claude results, the
// fetch budget and the last good server address still hold, and
// status.json reads as Claude's.
func TestOldStateFilesMigrate(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"latest.json", "backlog.json", "state.json", "status.json"} {
		b, err := os.ReadFile(filepath.Join("../../testdata/v081", f))
		must(t, err)
		must(t, os.WriteFile(filepath.Join(dir, f), b, 0o600))
	}
	at := time.Date(2026, 11, 24, 8, 41, 0, 0, time.UTC) // before the saved not_before (08:42)
	login := okLogin()
	login.ExpiresAt = at.Add(8 * time.Hour)
	r := &rig{clk: clock.NewFake(at), logins: &fakeLogins{login: login}, dir: dir}
	r.syncer = &fakeSyncer{fn: always(resp("poller", true, 60), nil)}
	r.fetcher = &fakeFetcher{clk: r.clk}
	bl, err := backlog.Open(filepath.Join(dir, "backlog.json"))
	must(t, err)
	r.backlog = bl
	r.e = r.build()
	if _, err := os.Stat(filepath.Join(dir, "latest.json")); !os.IsNotExist(err) {
		t.Fatal("latest.json is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "latest-claude.json")); err != nil {
		t.Fatal(err)
	}
	r.e.Step(ctx)
	eq(t, r.syncer.calls[0].base, "http://ai-usage.local") // state.json's last good address
	req := r.syncer.calls[0].req
	eq(t, len(req.Results), 2)
	eq(t, req.Results[0].AccountUUID, "acc-1")
	eq(t, req.Results[0].FetchedAt.Time, time.Date(2026, 11, 24, 8, 10, 0, 0, time.UTC))
	eq(t, req.Results[1].HTTPStatus, 429)
	eq(t, r.backlog.Len(), 0)      // delivered
	eq(t, r.fetcher.calls, 0)      // the saved 429 backoff still holds
	eq(t, *weekly(r.snap()), 30.0) // the migrated numbers are shown
	f, _ := r.cl().Row("weekly_scoped:Fable")
	eq(t, *f.Percent, 76.0)
	eq(t, r.cl().LatestFetchedAt, time.Date(2026, 11, 24, 8, 58, 0, 0, time.UTC))
	r.clk.Set(time.Date(2026, 11, 24, 8, 42, 0, 0, time.UTC))
	r.e.Step(ctx)
	eq(t, r.fetcher.calls, 1)
	// a restart reads the migrated file again; the old name is not recreated
	r.e = r.build()
	r.syncer.fn = always(protocol.Response{}, errDown)
	r.e.Step(ctx)
	eq(t, *weekly(r.snap()), 30.0) // 08:58 from latest-claude.json is newer than the 08:42 fetch
	if _, err := os.Stat(filepath.Join(dir, "latest.json")); !os.IsNotExist(err) {
		t.Fatal("latest.json came back")
	}
	s, err := status.Read(filepath.Join(dir, "status.json"))
	must(t, err)
	eq(t, s.Providers[provider.Claude].State, "ok")
	eq(t, s.Providers[provider.Claude].PeersPCs, 3)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// After a downgrade (v0.8 wrote latest.json again) and an upgrade, the
// newer of the two files is kept and the other removed (review m-L).
func TestLatestFilesAfterADowngradeKeepTheNewer(t *testing.T) {
	file := func(at string) string {
		return `{"fetched_at":"` + at + `","usage":{"seven_day":{"utilization":9}}}`
	}
	for _, c := range []struct{ legacy, claude, want string }{
		{"2026-11-24T09:30:00.000Z", "2026-11-24T08:00:00.000Z", "2026-11-24T09:30:00.000Z"},
		{"2026-11-24T07:00:00.000Z", "2026-11-24T08:00:00.000Z", "2026-11-24T08:00:00.000Z"},
	} {
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, "latest.json"), []byte(file(c.legacy)), 0o600))
		must(t, os.WriteFile(filepath.Join(dir, "latest-claude.json"), []byte(file(c.claude)), 0o600))
		r := &rig{clk: clock.NewFake(t0), logins: &fakeLogins{login: okLogin()}, dir: dir,
			syncer: &fakeSyncer{fn: always(protocol.Response{}, errDown)}}
		r.fetcher = &fakeFetcher{clk: r.clk}
		bl, err := backlog.Open(filepath.Join(dir, "backlog.json"))
		must(t, err)
		r.backlog = bl
		r.e = r.build()
		if _, err := os.Stat(filepath.Join(dir, "latest.json")); !os.IsNotExist(err) {
			t.Fatalf("%v: latest.json left behind", c)
		}
		b, err := os.ReadFile(filepath.Join(dir, "latest-claude.json"))
		must(t, err)
		if !strings.Contains(string(b), c.want) {
			t.Fatalf("%v: kept %s", c, b)
		}
	}
}

// Results of a provider this build has no lane for (a downgrade from a
// build that had one) take no place in the batch (review m-B).
func TestBacklogWithoutALaneTakesNoSlot(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	for i := range 3 {
		must(t, r.backlog.Add(codexItem(t0.Add(time.Duration(-30+i)*time.Minute))))
	}
	must(t, r.backlog.Add(backlog.Item{Provider: "claude", AccountKey: "acc-1", FetchedAt: protocol.Time{Time: t0.Add(-time.Minute)}, HTTPStatus: 200}))
	r.e.batchLimit = 2
	r.e.Step(ctx)
	req := r.syncer.calls[0].v2
	eq(t, len(req.Providers), 1)
	eq(t, len(req.Providers[0].Results), 1)
	eq(t, len(r.backlog.Pending(10, "claude")), 0)
	eq(t, len(r.backlog.Pending(10, "codex")), 3) // kept for a build that has the lane
}

// A reply without the weekly value sets parse_problem; once the shown
// numbers are replaced by readable ones from the server, the PC stops
// sending it (review m-S).
func TestParseProblemEndsWithReadableNumbers(t *testing.T) {
	n := 0
	later := t0.Add(5 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	r := newRig(t, func(int, string, protocol.Request) (protocol.Response, error) {
		n++
		if n == 1 {
			return resp("poller", true, 60), nil
		}
		a := resp("standby", false, 60)
		if n >= 3 {
			a.Latest = &protocol.Latest{FetchedAt: later, Usage: json.RawMessage(usageBody)}
		}
		return a, nil
	})
	r.fetcher.body = `{"limits":[]}`
	r.step() // sync, fetch (no weekly), sync
	eq(t, string(r.syncer.calls[1].v2.Providers[0].ParseProblem), "no_weekly")
	r.clk.Set(t0.Add(10 * time.Minute))
	r.step() // the server's numbers arrive
	r.step()
	last := r.syncer.calls[len(r.syncer.calls)-1].v2
	eq(t, string(last.Providers[0].ParseProblem), "")
}

// Review N-4: retry_after_seconds is capped at one day on the wire (the
// design's longest wait; the server refuses absurd values).
func TestRetryAfterIsCappedAtADay(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, errDown))
	r.logins.login = creds.Login{}
	other := &fakeProvider{id: provider.Codex, clk: r.clk, status: 429, wait: 400 * 24 * time.Hour}
	r.extra = []provider.Provider{other}
	r.e = r.build()
	for len(other.at) == 0 && r.clk.Now().Sub(t0) < time.Hour {
		r.step()
	}
	items := r.backlog.Pending(10, "codex")
	if len(items) == 0 || items[0].RetryAfterSeconds == nil {
		t.Fatalf("%+v", items)
	}
	eq(t, *items[0].RetryAfterSeconds, 86400)
}
