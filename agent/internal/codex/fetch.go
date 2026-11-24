package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// DefaultBaseURL is Codex's own default chatgpt_base_url.
const DefaultBaseURL = "https://chatgpt.com/backend-api/"

// ErrBaseURLNotAllowed: config.toml's chatgpt_base_url is not
// https://chatgpt.com or https://chat.openai.com, so the token is not sent
// (design D1; exact hosts, controller ruling on review m-7).
var ErrBaseURLNotAllowed = errors.New("codex: chatgpt_base_url is not chatgpt.com or chat.openai.com")

// UsageURL is the usage address for a chatgpt_base_url ("" for the
// default), or ErrBaseURLNotAllowed.
//
// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/Providers/Codex/CodexOAuth/CodexOAuthUsageFetcher.swift
// resolveUsageURL and normalizeChatGPTBaseURL @ a26ca57: trim, drop
// trailing "/", add "/backend-api" to https://chatgpt.com and
// https://chat.openai.com when missing; "/wham/usage" when the address
// has "/backend-api", else "/api/codex/usage". Our addition (CodexBar has
// none): the address must be https, without user, port, query or
// fragment, on chatgpt.com or chat.openai.com exactly (no other host,
// not even under those domains), so that a config file cannot send the
// token elsewhere.
func UsageURL(base string) (string, error) {
	b := strings.TrimSpace(base)
	if b == "" {
		b = DefaultBaseURL
	}
	for strings.HasSuffix(b, "/") {
		b = strings.TrimSuffix(b, "/")
	}
	// the scheme and host in lower case before they are compared (review
	// N-2: HTTPS://CHATGPT.COM got the path of a foreign host)
	if i := strings.Index(b, "://"); i > 0 {
		end := len(b)
		if j := strings.IndexAny(b[i+3:], "/?#"); j >= 0 {
			end = i + 3 + j
		}
		b = strings.ToLower(b[:end]) + b[end:]
	}
	if (strings.HasPrefix(b, "https://chatgpt.com") || strings.HasPrefix(b, "https://chat.openai.com")) && !strings.Contains(b, "/backend-api") {
		b += "/backend-api"
	}
	path := "/api/codex/usage"
	if strings.Contains(b, "/backend-api") {
		path = "/wham/usage"
	}
	u, err := allowed(b + path)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// allowed parses s and checks it against the allow-list.
func allowed(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Port() != "" || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(s, "\\?#") {
		return nil, ErrBaseURLNotAllowed
	}
	// exact hosts only (controller ruling on review m-7): other hosts under
	// openai.com and chatgpt.com include services run by third parties
	host := strings.ToLower(u.Hostname())
	if host != "chatgpt.com" && host != "chat.openai.com" {
		return nil, ErrBaseURLNotAllowed
	}
	u.Scheme, u.Host = "https", host
	return u, nil
}

// TokenSource gives the access token and the account id to fn for the
// duration of the call (Reader).
type TokenSource interface {
	WithToken(ctx context.Context, fn func(accessToken, accountID string) error) error
}

const (
	requestTimeout = 30 * time.Second
	maxBody        = 256 << 10
)

// Client asks one usage address. It is the only code that puts the Codex
// token on the wire: one request header, nowhere else.
type Client struct {
	url string
	ua  string
	clk clock.Clock
	hc  *http.Client
}

// NewClient asks url (from UsageURL) with our own User-Agent.
func NewClient(url, userAgent string, clk clock.Clock) *Client {
	return NewClientWith(url, userAgent, clk, nil)
}

// NewClientWith uses transport rt (nil: the default, which follows a
// company proxy from the environment, as the Claude request does).
func NewClientWith(url, userAgent string, clk clock.Clock, rt http.RoundTripper) *Client {
	if clk == nil {
		clk = clock.Real{}
	}
	if rt == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyFromEnvironment
		rt = tr
	}
	return &Client{url: url, ua: userAgent, clk: clk, hc: &http.Client{
		Transport:     rt,
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// At is the same client for another usage address.
func (c *Client) At(url string) *Client {
	n := *c
	n.url = url
	return &n
}

// URL is the address the client asks.
func (c *Client) URL() string { return c.url }

// Fetch asks for the usage once. It returns an error only when no request
// was made (the address is not allowed, or no usable sign-in); every
// request gives a Result.
//
// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/Providers/Codex/CodexOAuth/CodexOAuthUsageFetcher.swift
// fetchUsage @ a26ca57: GET with Authorization: Bearer, ChatGPT-Account-Id
// when known, Accept: application/json and the product's own User-Agent,
// 30 s. Ours also follows no redirect and keeps at most 256 KiB.
func (c *Client) Fetch(ctx context.Context, src TokenSource) (provider.Result, error) {
	u, err := allowed(c.url)
	if err != nil {
		return provider.Result{}, err
	}
	var res provider.Result
	attempted := false
	err = src.WithToken(ctx, func(token, account string) error {
		attempted = true
		res = c.do(ctx, u.String(), token, account)
		return nil
	})
	if !attempted {
		if err == nil {
			err = errors.New("codex usage fetch was not attempted")
		}
		return provider.Result{}, err
	}
	return res, nil
}

func (c *Client) do(ctx context.Context, u, token, account string) provider.Result {
	res := provider.Result{Provider: provider.Codex, Source: "wham", FetchedAt: c.clk.Now().UTC().Truncate(time.Millisecond)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		res.Problem = "usage request: bad URL"
		return res
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if account != "" {
		req.Header.Set("ChatGPT-Account-Id", account)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.hc.Do(req)
	if err != nil {
		res.Problem = "usage request failed: " + describe(err)
		return res
	}
	defer resp.Body.Close()
	res.HTTPStatus = resp.StatusCode
	res.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	switch {
	case err != nil:
		res.Problem = "usage response: body could not be read"
	case len(body) > maxBody:
		res.Problem = "usage response: body larger than 256 KiB"
	case resp.StatusCode == http.StatusOK:
		if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '{' && json.Valid(t) {
			res.Usage = json.RawMessage(t)
		} else {
			res.Problem = "usage response: body is not a JSON object"
		}
	}
	return res
}

// describe keeps the transport error without the request URL.
func describe(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var to interface{ Timeout() bool }
	if errors.As(err, &to) && to.Timeout() {
		return "timed out"
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// parseRetryAfter reads Retry-After in seconds or as an HTTP date (as
// internal/anthropic does for Claude).
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now).Round(time.Second); d > 0 {
			return d
		}
	}
	return 0
}
