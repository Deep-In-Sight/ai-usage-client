package codex

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// fakeOpenAI is a TLS test server standing in for every host: the client's
// transport dials it whatever the URL's host, so a request the allow-list
// should have refused would arrive here.
type fakeOpenAI struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	// reply writes the answer (nil: 200 with the weekly-only fixture)
	reply func(w http.ResponseWriter, r *http.Request)
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{}
	body, err := readTestdata("wham-weekly-only")
	must(t, err)
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(context.Background()))
		reply := f.reply
		f.mu.Unlock()
		if reply != nil {
			reply(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.Close)
	return f
}

// transport dials the fake server for every host and trusts its
// certificate (the certificate names example.com).
func (f *fakeOpenAI) transport() http.RoundTripper {
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	addr := f.Listener.Addr().String()
	return &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"},
	}
}

func (f *fakeOpenAI) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.reqs...)
}

func readTestdata(name string) ([]byte, error) {
	return readCapped("testdata/"+name+".json", 1<<20)
}

// tokens hands a fixed fake token to the request.
type tokens struct {
	access, account string
	err             error
	calls           int
}

func (s *tokens) WithToken(_ context.Context, fn func(string, string) error) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return fn(s.access, s.account)
}

func newTestClient(f *fakeOpenAI, base string) *Client {
	u, err := UsageURL(base)
	if err != nil {
		panic(err)
	}
	return NewClientWith(u, "ai-usage/0.0.0-test", clock.NewFake(t0), f.transport())
}

// The request has exactly our headers: the token as a Bearer, the account
// id, JSON, and our own User-Agent; nothing imitating Codex (design D1).
func TestFetchHeaders(t *testing.T) {
	f := newFakeOpenAI(t)
	c := newTestClient(f, "")
	res, err := c.Fetch(context.Background(), &tokens{access: fakeAccess, account: fakeAccountID})
	must(t, err)
	eq(t, res.HTTPStatus, 200)
	eq(t, res.Source, "wham")
	eq(t, res.FetchedAt, t0)
	if len(res.Usage) == 0 {
		t.Fatal("no usage")
	}
	reqs := f.requests()
	eq(t, len(reqs), 1)
	r := reqs[0]
	eq(t, r.Method, http.MethodGet)
	eq(t, r.Host, "chatgpt.com")
	eq(t, r.URL.Path, "/backend-api/wham/usage")
	eq(t, r.URL.RawQuery, "")
	want := map[string]string{"Authorization": "Bearer " + fakeAccess, "Chatgpt-Account-Id": fakeAccountID,
		"Accept": "application/json", "User-Agent": "ai-usage/0.0.0-test", "Accept-Encoding": "gzip"}
	for k := range r.Header {
		if _, ok := want[k]; !ok {
			t.Fatalf("unexpected header %s", k)
		}
	}
	for k, v := range want {
		eq(t, r.Header.Get(k), v)
	}
	// without an account id the header is left out
	_, err = c.Fetch(context.Background(), &tokens{access: fakeAccess})
	must(t, err)
	if _, ok := f.requests()[1].Header["Chatgpt-Account-Id"]; ok {
		t.Fatal("an empty ChatGPT-Account-Id")
	}
}

// RF5: chatgpt_base_url can only point at chatgpt.com or chat.openai.com
// over https;
// anything else sends nothing (the fake server, reachable for every host,
// hears no request).
func TestBaseURLOutsideAllowListSendsNothing(t *testing.T) {
	for base, want := range map[string]string{
		"":                                       "https://chatgpt.com/backend-api/wham/usage",
		"https://chatgpt.com":                    "https://chatgpt.com/backend-api/wham/usage",
		"https://chatgpt.com/":                   "https://chatgpt.com/backend-api/wham/usage",
		" https://chat.openai.com/backend-api/ ": "https://chat.openai.com/backend-api/wham/usage",
		"HTTPS://ChatGPT.com/backend-api":        "https://chatgpt.com/backend-api/wham/usage",
	} {
		got, err := UsageURL(base)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v, want %q", base, got, err, want)
		}
	}
	f := newFakeOpenAI(t)
	for _, base := range []string{"http://chatgpt.com", "https://evil.example", "https://chatgpt.com.evil.example",
		"https://openai.com.example", "https://evilchatgpt.com", "https://user@chatgpt.com", "https://chatgpt.com:8443",
		"ftp://chatgpt.com", "chatgpt.com", "https://chatgpt.com/backend-api?x=1", "https://chatgpt.com#x",
		"https://xn--chatgpt-.com", "https://chatgpt.com\\@evil.example", "https://[::1]",
		// exact hosts only (controller ruling on review m-7): other hosts
		// under openai.com or chatgpt.com include third parties' services
		"https://community.openai.com", "https://api.openai.com/backend-api", "https://ab.chatgpt.com",
		"https://chatgpt.com.", "https://help.openai.com/backend-api"} {
		if u, err := UsageURL(base); !errors.Is(err, ErrBaseURLNotAllowed) {
			t.Fatalf("%q allowed as %q (%v)", base, u, err)
		}
		c := NewClientWith(base, "ai-usage/0.0.0-test", clock.NewFake(t0), f.transport()) // a client given a bad URL directly
		src := &tokens{access: fakeAccess, account: fakeAccountID}
		if _, err := c.Fetch(context.Background(), src); !errors.Is(err, ErrBaseURLNotAllowed) {
			t.Fatalf("%q: %v", base, err)
		}
		eq(t, src.calls, 0) // the token was not even read
	}
	eq(t, len(f.requests()), 0)
}

// No redirect is followed: the token goes to the allowed host only.
func TestFetchFollowsNoRedirect(t *testing.T) {
	f := newFakeOpenAI(t)
	f.reply = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			t.Error("redirect followed")
		}
		http.Redirect(w, r, "https://evil.example/elsewhere", http.StatusFound)
	}
	res, err := newTestClient(f, "").Fetch(context.Background(), &tokens{access: fakeAccess})
	must(t, err)
	eq(t, res.HTTPStatus, http.StatusFound)
	eq(t, len(res.Usage), 0)
	eq(t, len(f.requests()), 1)
}

// A missing or expired sign-in sends nothing (RF2: after exp no request at
// all).
func TestExpiredMakesNoRequest(t *testing.T) {
	f := newFakeOpenAI(t)
	c := newTestClient(f, "")
	for _, e := range []error{ErrExpired, ErrNotSignedIn} {
		if _, err := c.Fetch(context.Background(), &tokens{err: e}); !errors.Is(err, e) {
			t.Fatal(err)
		}
	}
	// through the real reader: a token whose exp has passed
	home := t.TempDir()
	writeFile(t, home+"/auth.json", authJSON(t, t0.Add(-time.Minute)))
	r := newReader(home, clock.NewFake(t0))
	if _, err := c.Fetch(context.Background(), r); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	eq(t, len(f.requests()), 0)
	// 20 minutes left: the direct call is still made
	writeFile(t, home+"/auth.json", authJSON(t, t0.Add(20*time.Minute)))
	_, err := c.Fetch(context.Background(), newReader(home, clock.NewFake(t0)))
	must(t, err)
	eq(t, len(f.requests()), 1)
}

// HTTP answers become results: the status, the body only for a JSON
// object with 200, the Retry-After of a 429 in seconds or as a date.
func TestFetchAnswers(t *testing.T) {
	f := newFakeOpenAI(t)
	c := newTestClient(f, "")
	src := &tokens{access: fakeAccess, account: fakeAccountID}
	for _, a := range []struct {
		status      int
		body, retry string
		usage       bool
		wait        time.Duration
	}{
		{200, `{"rate_limit":{}}`, "", true, 0},
		{200, `<html>blocked</html>`, "", false, 0},
		{200, `[1,2]`, "", false, 0},
		{401, `{"detail":"Unauthorized"}`, "", false, 0},
		{403, `<html>cf</html>`, "", false, 0},
		{429, `{}`, "120", false, 120 * time.Second},
		{429, `{}`, time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat), false, 90 * time.Second},
		{500, `oops`, "", false, 0},
	} {
		f.reply = func(w http.ResponseWriter, r *http.Request) {
			if a.retry != "" {
				w.Header().Set("Retry-After", a.retry)
			}
			w.WriteHeader(a.status)
			_, _ = io.WriteString(w, a.body)
		}
		res, err := c.Fetch(context.Background(), src)
		must(t, err)
		eq(t, res.HTTPStatus, a.status)
		eq(t, len(res.Usage) > 0, a.usage)
		if d := res.RetryAfter - a.wait; d < -2*time.Second || d > 2*time.Second {
			t.Fatalf("%d: retry after %v, want %v", a.status, res.RetryAfter, a.wait)
		}
		if strings.Contains(res.Problem+res.Detail, fakeAccess) {
			t.Fatal("token in the result")
		}
	}
	// a body over 256 KiB is not kept
	f.reply = func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"x":"`+strings.Repeat("a", 300<<10)+`"}`)
	}
	res, err := c.Fetch(context.Background(), src)
	must(t, err)
	eq(t, len(res.Usage), 0)
	if res.Problem == "" {
		t.Fatal("no problem for an oversized body")
	}
}

// No answer within the time limit: a result without a status, the problem
// without the URL or the token.
func TestFetchTimeout(t *testing.T) {
	f := newFakeOpenAI(t)
	release := make(chan struct{})
	f.reply = func(w http.ResponseWriter, r *http.Request) { <-release }
	defer close(release)
	c := newTestClient(f, "")
	c.hc.Timeout = 100 * time.Millisecond
	res, err := c.Fetch(context.Background(), &tokens{access: fakeAccess})
	must(t, err)
	eq(t, res.HTTPStatus, 0)
	eq(t, res.Problem, "usage request failed: timed out")
}
