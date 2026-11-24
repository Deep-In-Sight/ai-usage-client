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

	cx "github.com/Deep-In-Sight/ai-usage-client/agent/internal/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// Fallback asks Codex's own CLI (codex app-server) when the direct
// request is refused; attempted is false when it did not run (design D1;
// plan Task 9). nil: none.
type Fallback interface {
	Fetch(ctx context.Context) (res provider.Result, attempted bool)
}

// Provider is Codex behind provider.Provider.
type Provider struct {
	reader   *cx.Reader
	client   *cx.Client
	fallback Fallback

	mu      sync.Mutex
	account string // the account of the last Login
}

// New reads the sign-in with reader and asks with client (its address is
// set from config.toml's chatgpt_base_url at each fetch).
func New(reader *cx.Reader, client *cx.Client, fallback Fallback) *Provider {
	return &Provider{reader: reader, client: client, fallback: fallback}
}

func (p *Provider) ID() provider.ID { return provider.Codex }

// usageURL is the address config.toml asks for, or ErrBaseURLNotAllowed.
func (p *Provider) usageURL() (string, error) {
	return cx.UsageURL(cx.ReadConfig(p.reader.Home()).ChatGPTBaseURL)
}

// Login is the sign-in (design D2); a usable one whose config.toml points
// the requests away from OpenAI is unsupported_base_url.
func (p *Provider) Login(ctx context.Context) provider.Login {
	l := p.reader.Login(ctx)
	if l.State == "ok" || l.State == "expired" {
		if _, err := p.usageURL(); err != nil {
			l.State, l.Problem = "unsupported_base_url", "chatgpt_base_url in Codex's config.toml is not an OpenAI address"
		}
	}
	p.mu.Lock()
	p.account = l.AccountKey
	p.mu.Unlock()
	return l
}

// Fetch asks chatgpt.com once (a refusal the CLI might get past goes to
// the fallback); the result names the account of the last Login. A 401
// marks the sign-in expired until Codex renews it.
func (p *Provider) Fetch(ctx context.Context) (provider.Result, error) {
	u, err := p.usageURL()
	if err != nil {
		return provider.Result{}, err
	}
	res, err := p.client.At(u).Fetch(ctx, p.reader)
	if err != nil {
		return provider.Result{}, err
	}
	switch {
	case res.HTTPStatus == http.StatusUnauthorized:
		p.reader.Rejected() // never the fallback: the CLI would refresh the token
	case needsFallback(res) && p.fallback != nil:
		if fr, ok := p.fallback.Fetch(ctx); ok {
			res = fr
		}
	}
	p.mu.Lock()
	res.Provider, res.AccountKey = provider.Codex, p.account
	p.mu.Unlock()
	return res, nil
}

// needsFallback: the direct request was turned away in a way the CLI's
// own route may get past (design D1): 403, 404, a 200 that is not JSON or
// has no rate limits at all.
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
		_, a := top["rate_limit"]
		_, b := top["additional_rate_limits"]
		return !a && !b
	}
	return false
}

// Parse gives the rows of a Codex reply (design D6, D7).
func (p *Provider) Parse(raw json.RawMessage) provider.Parsed { return cx.Parse(raw) }
