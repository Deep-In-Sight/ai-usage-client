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

// Sync sends one request to baseURL (scheme://host[:port]).
func (c *Client) Sync(ctx context.Context, baseURL string, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("sync: encode request: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/sync", bytes.NewReader(body))
	if err != nil {
		return Response{}, errors.New("sync: bad server URL")
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
		return Response{}, fmt.Errorf("sync: %s", describe(err))
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxReplySize+1))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return Response{}, ErrBadIngestKey
	case http.StatusBadRequest:
		return Response{}, ErrUnsupportedSchema
	default:
		return Response{}, &StatusError{Status: resp.StatusCode}
	}
	if rerr != nil {
		return Response{}, fmt.Errorf("%w: body could not be read", ErrBadResponse)
	}
	if len(data) > maxReplySize {
		return Response{}, fmt.Errorf("%w: larger than 1 MiB", ErrBadResponse)
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
