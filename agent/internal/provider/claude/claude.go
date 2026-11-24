// Package claude is the Claude provider: the Claude Code sign-in
// (internal/creds), the usage request (internal/anthropic) and the reply's
// rows (internal/usage) behind provider.Provider. The token passes from the
// sign-in reader to the request inside one callback and nowhere else.
package claude

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Logins gives the current Claude Code sign-in (without the token).
type Logins interface {
	Current(ctx context.Context) creds.Login
}

// Fetcher asks Anthropic for the usage once.
type Fetcher interface {
	Fetch(ctx context.Context) (anthropic.Result, error)
}

// fetcher hands the reader's token to the usage client inside the call.
type fetcher struct {
	client *anthropic.Client
	src    anthropic.TokenSource
}

func (f fetcher) Fetch(ctx context.Context) (anthropic.Result, error) {
	return f.client.Fetch(ctx, f.src)
}

// Provider is Claude behind provider.Provider.
type Provider struct {
	logins  Logins
	fetcher Fetcher
	clk     clock.Clock

	mu      sync.Mutex
	account string // the account of the last Login
}

// New reads the sign-in with reader and asks Anthropic with client.
func New(reader *creds.Reader, client *anthropic.Client, clk clock.Clock) *Provider {
	return Wrap(reader, fetcher{client: client, src: reader}, clk)
}

// Wrap makes the provider from a sign-in source and a fetcher (tests give
// fakes).
func Wrap(l Logins, f Fetcher, clk clock.Clock) *Provider {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Provider{logins: l, fetcher: f, clk: clk}
}

func (p *Provider) ID() provider.ID { return provider.Claude }

// Login maps the Claude Code sign-in; the state is taken at the clock's now.
func (p *Provider) Login(ctx context.Context) provider.Login {
	l := p.logins.Current(ctx)
	out := provider.Login{Provider: provider.Claude, State: l.State(p.clk.Now()), Plan: l.SubscriptionType,
		RateLimitTier: l.RateLimitTier, ExpiresAt: l.ExpiresAt, Problem: l.Problem}
	if a := l.Account; a != nil {
		out.AccountKey, out.Email, out.DisplayName = a.UUID, a.Email, a.DisplayName
		out.OrganizationName, out.OrganizationType = a.OrganizationName, a.OrganizationType
	}
	p.mu.Lock()
	p.account = out.AccountKey
	p.mu.Unlock()
	return out
}

// Fetch asks Anthropic once; the result names the account of the last
// Login.
func (p *Provider) Fetch(ctx context.Context) (provider.Result, error) {
	res, err := p.fetcher.Fetch(ctx)
	if err != nil {
		return provider.Result{}, err
	}
	p.mu.Lock()
	account := p.account
	p.mu.Unlock()
	return provider.Result{Provider: provider.Claude, AccountKey: account, FetchedAt: res.FetchedAt, HTTPStatus: res.HTTPStatus,
		Usage: res.Usage, RetryAfter: res.RetryAfter, Problem: res.Problem}, nil
}

// Parse gives the rows of a Claude reply (design D5): 5-hour when it has a
// value, Weekly always, Fable when it has a value. The main limit is
// weekly_all.
func (p *Provider) Parse(raw json.RawMessage) provider.Parsed {
	l := usage.ParseLimits(raw, usage.ScopedModel)
	row := func(key, label string, lim usage.Limit, mins int) provider.Row {
		return provider.Row{Key: key, Label: label, Percent: lim.Percent, ResetsAt: lim.ResetsAt, Level: usage.LevelOf(lim), WindowMins: mins}
	}
	var rows []provider.Row
	if l.FiveHour.Percent != nil {
		rows = append(rows, row("five_hour", "5-hour", l.FiveHour, 300))
	}
	rows = append(rows, row("weekly_all", "Weekly", l.Weekly, 10080))
	if l.Scoped.Percent != nil {
		rows = append(rows, row("weekly_scoped:"+usage.ScopedModel, usage.ScopedModel, l.Scoped, 10080))
	}
	return provider.Parsed{Rows: rows, MainKey: "weekly_all", ParseProblem: l.Problem}
}
