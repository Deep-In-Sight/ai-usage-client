package claude

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

var t0 = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

// stage0 is the reply shape this host's account gives (fake numbers).
const stage0 = `{"five_hour":null,"seven_day":{"utilization":30.0,"resets_at":"2026-11-28T16:00:00+00:00"},
 "limits":[{"kind":"weekly_all","percent":30,"severity":"normal","resets_at":"2026-11-28T16:00:00+00:00","scope":null},
 {"kind":"weekly_scoped","percent":25,"severity":"warning","resets_at":"2026-11-28T16:00:00+00:00","scope":{"model":{"display_name":"Fable"}}}]}`

func TestClaudeRowsHideAbsent(t *testing.T) {
	p := Wrap(nil, nil, clock.NewFake(t0)).Parse(json.RawMessage(stage0))
	if len(p.Rows) != 2 || p.Rows[0].Key != "weekly_all" || p.Rows[1].Key != "weekly_scoped:Fable" {
		t.Fatalf("rows %+v", p.Rows)
	}
	w, f := p.Rows[0], p.Rows[1]
	if w.Label != "Weekly" || *w.Percent != 30 || w.WindowMins != 10080 || w.Level != usage.LevelNormal || w.ResetsAt == nil {
		t.Fatalf("weekly %+v", w)
	}
	if f.Label != "Fable" || *f.Percent != 25 || f.Level != usage.LevelWarning {
		t.Fatalf("fable %+v", f)
	}
	if p.MainKey != "weekly_all" || p.ParseProblem != "" {
		t.Fatalf("%+v", p)
	}
	// with a 5-hour value it comes first; with nothing the weekly row stays
	q := Wrap(nil, nil, nil).Parse(json.RawMessage(`{"limits":[{"kind":"session","percent":100,"resets_at":"2026-11-24T14:00:00Z"},{"kind":"weekly_all","percent":3,"resets_at":"2026-11-28T16:00:00Z"}]}`))
	if len(q.Rows) != 2 || q.Rows[0].Key != "five_hour" || q.Rows[0].Label != "5-hour" || q.Rows[0].WindowMins != 300 {
		t.Fatalf("rows %+v", q.Rows)
	}
	if r, _ := provider.Shown(q); r.Key != "five_hour" {
		t.Fatalf("shown %+v", r)
	}
	e := Wrap(nil, nil, nil).Parse(json.RawMessage(`{"limits":[]}`))
	if len(e.Rows) != 1 || e.Rows[0].Percent != nil || e.ParseProblem != "no_weekly" {
		t.Fatalf("%+v", e)
	}
	if n := Wrap(nil, nil, nil).Parse(nil); len(n.Rows) != 1 || n.ParseProblem != "" {
		t.Fatalf("%+v", n)
	}
}

type fakeLogins struct{ l creds.Login }

func (f fakeLogins) Current(context.Context) creds.Login { return f.l }

type fakeFetcher struct {
	res anthropic.Result
	err error
}

func (f fakeFetcher) Fetch(context.Context) (anthropic.Result, error) { return f.res, f.err }

func TestLoginCarriesNoToken(t *testing.T) {
	clk := clock.NewFake(t0)
	l := creds.Login{HasToken: true, ExpiresAt: t0.Add(time.Hour), SubscriptionType: "max", RateLimitTier: "tier",
		Account: &creds.Account{UUID: "acc-1", Email: "a@example.com", DisplayName: "A", OrganizationName: "O", OrganizationType: "claude_max"}}
	p := Wrap(fakeLogins{l}, fakeFetcher{}, clk)
	got := p.Login(context.Background())
	want := provider.Login{Provider: provider.Claude, State: "ok", AccountKey: "acc-1", Email: "a@example.com", DisplayName: "A",
		OrganizationName: "O", OrganizationType: "claude_max", Plan: "max", RateLimitTier: "tier", ExpiresAt: t0.Add(time.Hour)}
	if got != want {
		t.Fatalf("got %+v", got)
	}
	clk.Advance(2 * time.Hour)
	if s := p.Login(context.Background()).State; s != "expired" {
		t.Fatal(s)
	}
	if s := Wrap(fakeLogins{creds.Login{Problem: "none"}}, fakeFetcher{}, clk).Login(context.Background()); s.State != "not_signed_in" || s.Problem != "none" || s.AccountKey != "" {
		t.Fatalf("%+v", s)
	}
}

func TestFetchNamesTheAccount(t *testing.T) {
	clk := clock.NewFake(t0)
	l := creds.Login{HasToken: true, Account: &creds.Account{UUID: "acc-1"}}
	p := Wrap(fakeLogins{l}, fakeFetcher{res: anthropic.Result{FetchedAt: t0, HTTPStatus: 429, RetryAfter: 30 * time.Second}}, clk)
	p.Login(context.Background())
	r, err := p.Fetch(context.Background())
	if err != nil || r.Provider != provider.Claude || r.AccountKey != "acc-1" || r.HTTPStatus != 429 || r.RetryAfter != 30*time.Second || r.Source != "" {
		t.Fatalf("%+v %v", r, err)
	}
	q := Wrap(fakeLogins{l}, fakeFetcher{err: errors.New("not signed in")}, clk)
	if _, err := q.Fetch(context.Background()); err == nil {
		t.Fatal("no error when nothing was attempted")
	}
}
