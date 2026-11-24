package codex

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	cx "github.com/Deep-In-Sight/ai-usage-client/agent/internal/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

var t0 = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

const (
	fakeAccess    = "fake-codex-access-CCCCCCCCCCCCCCCC"
	fakeAccountID = "00000000-0000-4000-8000-000000000002"
	fakeUserID    = "user-FakeProvider01"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

// signIn writes an auth.json whose token expires at exp.
func signIn(t *testing.T, home string, exp time.Time) {
	t.Helper()
	access := jwt(map[string]any{"exp": exp.Unix(), "fake": fakeAccess})
	id := jwt(map[string]any{"email": "user-e@example.com", "https://api.openai.com/auth": map[string]any{
		"chatgpt_user_id": fakeUserID, "chatgpt_plan_type": "plus"}})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": access,
		"account_id": fakeAccountID, "id_token": id, "refresh_token": "fake-refresh-DDDDDDDDDDDDDDDD"}})
	must(t, os.MkdirAll(home, 0o700))
	must(t, os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600))
}

// server stands in for every host (its transport dials it whatever the
// URL), so a refused address would show up as a request here.
type server struct {
	*httptest.Server
	mu     sync.Mutex
	n      int
	status int
	body   string
	delay  time.Duration
}

func newServer(t *testing.T) *server {
	s := &server{status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":38,"limit_window_seconds":604800,"reset_at":1768046400}}}`}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.n++
		status, body, delay := s.status, s.body, s.delay
		s.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) set(status int, body string) {
	s.mu.Lock()
	s.status, s.body = status, body
	s.mu.Unlock()
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *server) transport() http.RoundTripper {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	addr := s.Listener.Addr().String()
	return &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"}}
}

// fallback is a fake Fallback: ready unless notReady, answering res.
type fallback struct {
	calls    int
	res      provider.Result
	ok       bool
	notReady bool
	got      cx.Auth
}

func (f *fallback) Ready(cx.Auth) bool { return !f.notReady }
func (f *fallback) Fetch(_ context.Context, a cx.Auth) (provider.Result, bool) {
	f.calls++
	f.got = a
	return f.res, f.ok
}
func (f *fallback) Status() (string, string) { return "/fake/codex 9.9.9", "" }

func newProvider(t *testing.T, s *server, fb Fallback) (*Provider, string, *clock.Fake) {
	t.Helper()
	home := t.TempDir()
	clk := clock.NewFake(t0)
	r := cx.NewReader(home, clk)
	c := cx.NewClientWith(cx.DefaultBaseURL, "ai-usage/0.0.0-test", clk, s.transport())
	return New(r, c, fb), home, clk
}

func TestFetchAndRows(t *testing.T) {
	s := newServer(t)
	p, home, _ := newProvider(t, s, nil)
	signIn(t, home, t0.Add(9*24*time.Hour))
	ctx := context.Background()
	l := p.Login(ctx)
	eq(t, l.State, "ok")
	eq(t, l.AccountKey, "codex:"+fakeAccountID+":"+fakeUserID)
	res, err := p.Fetch(ctx)
	must(t, err)
	eq(t, res.Provider, provider.Codex)
	eq(t, res.AccountKey, l.AccountKey)
	eq(t, res.HTTPStatus, 200)
	eq(t, res.Source, "wham")
	parsed := p.Parse(res.Usage)
	eq(t, parsed.MainKey, "codex:weekly")
	eq(t, len(parsed.Rows), 1)
}

// A 401 counts as expired until auth.json changes; the fallback is not
// used (design D2: Codex renews its sign-in, the agent waits).
func Test401MarksExpiredNoFallback(t *testing.T) {
	s := newServer(t)
	s.set(401, `{"detail":"Unauthorized"}`)
	fb := &fallback{}
	p, home, _ := newProvider(t, s, fb)
	signIn(t, home, t0.Add(9*24*time.Hour))
	ctx := context.Background()
	eq(t, p.Login(ctx).State, "ok")
	res, err := p.Fetch(ctx)
	must(t, err)
	eq(t, res.HTTPStatus, 401)
	eq(t, fb.calls, 0)
	l := p.Login(ctx)
	eq(t, l.State, "expired")
	if _, err := p.Fetch(ctx); !errors.Is(err, cx.ErrExpired) {
		t.Fatalf("fetched again after a 401: %v", err)
	}
	eq(t, s.count(), 1)
	// Codex renewed it: auth.json changed
	s.set(200, `{"rate_limit":{}}`)
	signIn(t, home, t0.Add(10*24*time.Hour))
	later := time.Now().Add(time.Hour)
	must(t, os.Chtimes(filepath.Join(home, "auth.json"), later, later))
	eq(t, p.Login(ctx).State, "ok")
	_, err = p.Fetch(ctx)
	must(t, err)
	eq(t, s.count(), 2)
}

// 403, 404, a 200 that is not JSON or has no rate limits go to the
// fallback (design D1); its result is the one reported when it ran.
func Test403UsesFallback(t *testing.T) {
	for _, c := range []struct {
		status    int
		body      string
		fallsBack bool
	}{
		{403, `<html>blocked</html>`, true},
		{404, `{}`, true},
		{200, `<html>login</html>`, true},
		{200, `{"plan_type":"plus"}`, true},
		// review m-6: null or empty rate limits are none (D1)
		{200, `{"rate_limit":null,"additional_rate_limits":null}`, true},
		{200, `{"additional_rate_limits":[]}`, true},
		{200, `{"rate_limit":null}`, true},
		{200, `{"rate_limit":{}}`, false},
		{200, `{"rate_limit":null,"additional_rate_limits":[{"limit_name":"x"}]}`, false},
		{429, `{}`, false},
		{500, `{}`, false},
	} {
		s := newServer(t)
		s.set(c.status, c.body)
		fb := &fallback{res: provider.Result{Provider: provider.Codex, HTTPStatus: 200, Source: "app-server", Usage: json.RawMessage(`{"rateLimits":{}}`)}, ok: true}
		p, home, _ := newProvider(t, s, fb)
		signIn(t, home, t0.Add(9*24*time.Hour))
		p.Login(context.Background())
		res, err := p.Fetch(context.Background())
		must(t, err)
		if c.fallsBack {
			eq(t, fb.calls, 1)
			eq(t, res.Source, "app-server")
			eq(t, res.AccountKey, "codex:"+fakeAccountID+":"+fakeUserID)
		} else {
			eq(t, fb.calls, 0)
			eq(t, res.HTTPStatus, c.status)
		}
		// a fallback that did not run leaves the direct result
		fb.ok, fb.calls = false, 0
		res, err = p.Fetch(context.Background())
		must(t, err)
		eq(t, res.Source, "wham")
	}
	// no fallback at all (this stage): the direct result
	s := newServer(t)
	s.set(403, `x`)
	p, home, _ := newProvider(t, s, nil)
	signIn(t, home, t0.Add(9*24*time.Hour))
	p.Login(context.Background())
	res, err := p.Fetch(context.Background())
	must(t, err)
	eq(t, res.HTTPStatus, 403)
}

// RF5 at the provider: a foreign chatgpt_base_url is the state
// unsupported_base_url and no request is made.
func TestForeignBaseURL(t *testing.T) {
	s := newServer(t)
	p, home, _ := newProvider(t, s, nil)
	signIn(t, home, t0.Add(9*24*time.Hour))
	must(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(`chatgpt_base_url = "https://proxy.example/backend-api/"`+"\n"), 0o600))
	ctx := context.Background()
	l := p.Login(ctx)
	eq(t, l.State, "unsupported_base_url")
	eq(t, l.AccountKey, "codex:"+fakeAccountID+":"+fakeUserID) // the account is known
	if _, err := p.Fetch(ctx); !errors.Is(err, cx.ErrBaseURLNotAllowed) {
		t.Fatal(err)
	}
	eq(t, s.count(), 0)
	// an allowed one is used
	must(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(`chatgpt_base_url = "https://chat.openai.com"`+"\n"), 0o600))
	eq(t, p.Login(ctx).State, "ok")
	_, err := p.Fetch(ctx)
	must(t, err)
	eq(t, s.count(), 1)
}
