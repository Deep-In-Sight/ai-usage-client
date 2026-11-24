package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrBadIngestKey      = errors.New("server rejected the ingest key")
	ErrUnsupportedSchema = errors.New("server does not accept this protocol version")
	ErrBadResponse       = errors.New("server reply is not a valid sync response")
)

// StatusError is any other non-200 answer, including redirects.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("sync: HTTP %d", e.Status) }

// DialFunc opens a connection; it lets the agent resolve .local names itself.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

const (
	syncTimeout  = 15 * time.Second
	dialTimeout  = 5 * time.Second
	maxReplySize = 1 << 20
)

// Client talks to the company server.
type Client struct {
	key string
	ua  string
	hc  *http.Client

	extraHeader func(http.Header) // tests only
}

// NewClient returns a client. Requests ignore proxy environment variables
// and never follow redirects. A nil dial uses the default dialer.
func NewClient(ingestKey, userAgent string, dial DialFunc) *Client {
	if dial == nil {
		d := &net.Dialer{Timeout: dialTimeout}
		dial = d.DialContext
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: syncTimeout,
	}
	return &Client{
		key: ingestKey,
		ua:  userAgent,
		hc: &http.Client{
			Transport:     tr,
			Timeout:       syncTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Sync sends one schema 1 request to baseURL (scheme://host[:port]).
func (c *Client) Sync(ctx context.Context, baseURL string, req Request) (Response, error) {
	data, err := c.post(ctx, baseURL, req, true)
	if err != nil {
		return Response{}, err
	}
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return Response{}, fmt.Errorf("%w: not JSON", ErrBadResponse)
	}
	if out.Role != "poller" && out.Role != "standby" {
		return Response{}, fmt.Errorf("%w: unknown role", ErrBadResponse)
	}
	return out, nil
}

// SyncV2 sends one schema 2 request. A server that does not speak schema 2
// answers 400 {"error":"unsupported_schema"} (a v5 server), or a reply
// without "providers" (a server that ignored the schema): both give
// ErrUnsupportedSchema.
func (c *Client) SyncV2(ctx context.Context, baseURL string, req RequestV2) (ResponseV2, error) {
	data, err := c.post(ctx, baseURL, req, false)
	if err != nil {
		return ResponseV2{}, err
	}
	var out struct {
		ResponseV2
		Providers *[]ProviderReply `json:"providers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return ResponseV2{}, fmt.Errorf("%w: not JSON", ErrBadResponse)
	}
	if out.Providers == nil {
		return ResponseV2{}, ErrUnsupportedSchema
	}
	resp := out.ResponseV2
	resp.Providers = *out.Providers
	for _, p := range resp.Providers {
		if p.Role != "poller" && p.Role != "standby" {
			return ResponseV2{}, fmt.Errorf("%w: unknown role", ErrBadResponse)
		}
	}
	return resp, nil
}

// post sends a request body and returns the reply of a 200. For schema 1
// any 400 is ErrUnsupportedSchema (as before); otherwise only a 400 whose
// body says "unsupported_schema".
func (c *Client) post(ctx context.Context, baseURL string, req any, any400 bool) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("sync: encode request: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/sync", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("sync: bad server URL")
	}
	if c.key != "" {
		hr.Header.Set("Authorization", "Bearer "+c.key)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("User-Agent", c.ua)
	if c.extraHeader != nil {
		c.extraHeader(hr.Header)
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("sync: %s", describe(err))
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxReplySize+1))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrBadIngestKey
	case http.StatusBadRequest:
		var e struct {
			Error string `json:"error"`
		}
		if any400 || json.Unmarshal(data, &e) == nil && e.Error == "unsupported_schema" {
			return nil, ErrUnsupportedSchema
		}
		return nil, &StatusError{Status: resp.StatusCode}
	default:
		return nil, &StatusError{Status: resp.StatusCode}
	}
	if rerr != nil {
		return nil, fmt.Errorf("%w: body could not be read", ErrBadResponse)
	}
	if len(data) > maxReplySize {
		return nil, fmt.Errorf("%w: larger than 1 MiB", ErrBadResponse)
	}
	return data, nil
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
