// Package anthropic calls the plan usage endpoint. It is the only package
// that puts the access token on the wire: the token arrives in the callback
// of TokenSource.WithToken and goes into one request header, nowhere else.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// UsageURL is fixed at compile time; config files cannot change it.
const UsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	requestTimeout = 20 * time.Second
	maxBody        = 1 << 20
)

// TokenSource gives the access token to fn for the duration of the call.
type TokenSource interface {
	WithToken(ctx context.Context, fn func(token string) error) error
}

// Result is one fetch attempt.
type Result struct {
	FetchedAt  time.Time       // just before the request, UTC, milliseconds
	HTTPStatus int             // 0: no response
	Usage      json.RawMessage // only for 200 with a JSON object body
	RetryAfter time.Duration
	Problem    string // never contains the token
}

// Client fetches usage.
type Client struct {
	url string
	ua  string
	clk clock.Clock
	hc  *http.Client
}

// New returns a client for url (UsageURL outside tests).
func New(url, userAgent string, clk clock.Clock) *Client { return NewWith(url, userAgent, clk, nil) }

// NewWith uses transport rt (nil: the default, which follows a company
// proxy from the environment).
func NewWith(url, userAgent string, clk clock.Clock, rt http.RoundTripper) *Client {
	if rt == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyFromEnvironment // the internet side follows a company proxy
		rt = tr
	}
	return &Client{
		url: url,
		ua:  userAgent,
		clk: clk,
		hc: &http.Client{
			Transport:     rt,
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Fetch asks for the usage once. It returns an error only when no request
// was attempted (no sign-in, expired, keychain failure); every attempt gives
// a Result.
func (c *Client) Fetch(ctx context.Context, src TokenSource) (Result, error) {
	var res Result
	attempted := false
	err := src.WithToken(ctx, func(token string) error {
		attempted = true
		res = c.do(ctx, token)
		return nil
	})
	if !attempted {
		if err == nil {
			err = errors.New("usage fetch was not attempted")
		}
		return Result{}, err
	}
	return res, nil
}

func (c *Client) do(ctx context.Context, token string) Result {
	res := Result{FetchedAt: c.clk.Now().UTC().Truncate(time.Millisecond)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		res.Problem = "usage request: bad URL"
		return res
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
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
		res.Problem = "usage response: body larger than 1 MiB"
	case resp.StatusCode == http.StatusOK:
		if isJSONObject(body) {
			res.Usage = json.RawMessage(body)
		} else {
			res.Problem = "usage response: body is not a JSON object"
		}
	}
	return res
}

func isJSONObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
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

func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(min(n, 7*24*3600)) * time.Second // no overflow from an absurd value
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now).Round(time.Second); d > 0 {
			return d
		}
	}
	return 0
}

// String never shows the usage body (it is not secret, but it is long).
func (r Result) String() string {
	return fmt.Sprintf("fetch %s status %d", r.FetchedAt.Format(time.RFC3339), r.HTTPStatus)
}
