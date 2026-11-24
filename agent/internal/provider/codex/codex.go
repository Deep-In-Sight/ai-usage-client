// Package codex is the Codex provider: the Codex CLI's sign-in
// (internal/codex Reader, read-only), the direct usage request to
// chatgpt.com (internal/codex Client) and the reply's rows behind
// provider.Provider. The token passes from the reader to the request
// inside one callback and nowhere else (design D1, D2).
package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	cx "github.com/Deep-In-Sight/ai-usage-client/agent/internal/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// Fallback asks Codex's own CLI (codex app-server) when the direct
// request is unusable (design D1; internal/codex AppServer). attempted is
// false when it did not run.
type Fallback interface {
	// Ready: Fetch would run now for sign-in a (it runs nothing).
	Ready(a cx.Auth) bool
	Fetch(ctx context.Context, a cx.Auth) (res provider.Result, attempted bool)
	// Status: the executable it would run and why it cannot, if so.
	Status() (cli, problem string)
}

// The Codex lane's budget (controller ruling R1). The engine caps a fetch
// at 35 s; when the fallback could run, the direct request gets
// DirectLimitWithFallback of it, so that the fallback still has its
// minimum, codex's exit grace and the reap inside the cap
// (TestCodexBudgetFitsTheEngineCap in the engine). Without a possible
// fallback the direct request keeps its own 30 s.
const DirectLimitWithFallback = 15 * time.Second

// DirectAgainAfter: once the direct request was unusable and the fallback
// answered, the fallback is asked first and the direct request is tried
// again after this long (design D1: every 6 hours).
const DirectAgainAfter = 6 * time.Hour

// Provider is Codex behind provider.Provider.
type Provider struct {
	reader   *cx.Reader
	client   *cx.Client
	fallback Fallback
	// DirectLimit is DirectLimitWithFallback (tests shorten it).
	DirectLimit time.Duration

	mu          sync.Mutex
	account     string    // the account of the last Login
	directAgain time.Time // the fallback first until then (zero: direct first)
}

// New reads the sign-in with reader and asks with client (its address is
// set from config.toml's chatgpt_base_url at each fetch); fallback may be
// nil (none).
func New(reader *cx.Reader, client *cx.Client, fallback Fallback) *Provider {
	if f, ok := fallback.(*cx.AppServer); ok && f == nil {
		fallback = nil
	}
	return &Provider{reader: reader, client: client, fallback: fallback, DirectLimit: DirectLimitWithFallback}
}

func (p *Provider) ID() provider.ID { return provider.Codex }

// usageURL is the address config.toml asks for, or ErrBaseURLNotAllowed.
func (p *Provider) usageURL() (string, error) {
	return cx.UsageURL(cx.ReadConfig(p.reader.Home()).ChatGPTBaseURL)
}

// Login is the sign-in (design D2); a usable one whose config.toml points
// the requests away from OpenAI is unsupported_base_url. While Codex is
// signed in it also names the fallback's executable (looked for at most
// every 10 minutes; its version asked only when it changed).
func (p *Provider) Login(ctx context.Context) provider.Login {
	l := p.reader.Login(ctx)
	if l.State == "ok" || l.State == "expired" {
		if _, err := p.usageURL(); err != nil {
			l.State, l.Problem = "unsupported_base_url", "chatgpt_base_url in Codex's config.toml is not an OpenAI address"
		} else if p.fallback != nil {
			l.FallbackCLI, l.FallbackProblem = p.fallback.Status()
		}
	}
	p.mu.Lock()
	p.account = l.AccountKey
	p.mu.Unlock()
	return l
}

// Fetch asks chatgpt.com once; when that is unusable in a way Codex's own
// CLI may get past (needsFallback), it asks the fallback, which runs only
// while the token has more than 30 minutes left (design D1, D2). A 401
// marks the sign-in expired until Codex renews it and never reaches the
// fallback. After the fallback answered for an unusable direct request,
// the fallback is asked first for DirectAgainAfter. The whole fetch ends
// inside ctx's deadline (the engine's cap).
func (p *Provider) Fetch(ctx context.Context) (provider.Result, error) {
	u, err := p.usageURL()
	if err != nil {
		return provider.Result{}, err
	}
	now := p.reader.Now()
	ready := false
	if p.fallback != nil {
		if a, ok := p.reader.Usable(ctx); ok {
			ready = p.fallback.Ready(a)
		}
	}
	p.mu.Lock()
	fallbackFirst := !p.directAgain.IsZero() && now.Before(p.directAgain)
	account := p.account
	p.mu.Unlock()
	if fallbackFirst && ready {
		if res, ok := p.askFallback(ctx); ok {
			return p.finish(res), nil
		}
	}
	dctx := ctx
	if ready {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, p.DirectLimit)
		defer cancel()
	}
	// the token only while auth.json still names the Login's account: the
	// result is filed under it (review m-2)
	res, err := p.client.At(u).Fetch(dctx, p.reader.For(account))
	if err != nil {
		return provider.Result{}, err
	}
	switch {
	case res.HTTPStatus == http.StatusUnauthorized:
		p.reader.Rejected() // never the fallback: the token was refused (design D2)
	case needsFallback(res) && p.fallback != nil:
		if ready {
			if fr, ok := p.askFallback(ctx); ok {
				res = fr
				p.mu.Lock()
				if !fallbackFirst {
					p.directAgain = now.Add(DirectAgainAfter)
				}
				p.mu.Unlock()
			}
		}
	case res.HTTPStatus == http.StatusOK:
		p.mu.Lock()
		p.directAgain = time.Time{} // the direct request works (again)
		p.mu.Unlock()
	}
	return p.finish(res), nil
}

// askFallback reads the sign-in once more right before codex would start
// (it must still be usable: not refused, more than 30 minutes left) and
// asks the fallback. Codex's own "authentication required" (401) counts
// as a refusal of the sign-in too.
func (p *Provider) askFallback(ctx context.Context) (provider.Result, bool) {
	a, ok := p.reader.Usable(ctx)
	p.mu.Lock()
	same := cx.AccountKey(a) == p.account
	p.mu.Unlock()
	if !ok || !same { // another account by now: its numbers must not be filed under the Login's (review m-2)
		return provider.Result{}, false
	}
	res, ok := p.fallback.Fetch(ctx, a)
	if ok && res.HTTPStatus == http.StatusUnauthorized {
		p.reader.Rejected()
	}
	return res, ok
}

func (p *Provider) finish(res provider.Result) provider.Result {
	p.mu.Lock()
	res.Provider, res.AccountKey = provider.Codex, p.account
	p.mu.Unlock()
	return res
}

// needsFallback: the direct request was turned away in a way the CLI's
// own route may get past (design D1): 403, 404, a 200 that is not JSON or
// has no rate limits at all (rate_limit and additional_rate_limits both
// missing, null or empty).
func needsFallback(res provider.Result) bool {
	switch res.HTTPStatus {
	case http.StatusForbidden, http.StatusNotFound:
		return true
	case http.StatusOK:
		if res.Usage == nil {
			return true
		}
		var top map[string]json.RawMessage
		if json.Unmarshal(res.Usage, &top) != nil {
			return true
		}
		return !limits(top["rate_limit"]) && !limits(top["additional_rate_limits"])
	}
	return false
}

// limits: a rate-limit value that holds something, an object or a
// non-empty array; null or missing is none (review m-6: the real Pro
// reply has additional_rate_limits null).
func limits(v json.RawMessage) bool {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return false
	}
	switch t := x.(type) {
	case map[string]any:
		return true
	case []any:
		return len(t) > 0
	}
	return false
}

// Parse gives the rows of a Codex reply (design D6, D7).
func (p *Provider) Parse(raw json.RawMessage) provider.Parsed { return cx.Parse(raw) }
