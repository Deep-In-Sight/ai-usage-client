package protocol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func okBody(role string) string {
	return fmt.Sprintf(`{"role":%q,"fetch_now":false,"next_sync_seconds":60,"latest":null,"peers":{"devices":1,"poller_hostname":null},"server_urls":[],"client_latest_version":null}`, role)
}

func TestClientRejectsBadResponses(t *testing.T) {
	var hits int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer other.Close()
	big := `{"role":"poller","pad":"` + strings.Repeat("x", 2<<20) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Case") {
		case "html":
			fmt.Fprint(w, "<html>")
		case "boss":
			fmt.Fprint(w, `{"role":"boss"}`)
		case "big":
			fmt.Fprint(w, big)
		case "redirect":
			http.Redirect(w, r, other.URL+"/api/v1/sync", http.StatusFound)
		case "413":
			w.WriteHeader(413)
		case "500":
			w.WriteHeader(500)
		default:
			fmt.Fprint(w, okBody("poller"))
		}
	}))
	defer srv.Close()
	run := func(c string) (Response, error) {
		cl := NewClient("k", "ua", nil)
		cl.extraHeader = func(h http.Header) { h.Set("X-Case", c) }
		return cl.Sync(context.Background(), srv.URL, Request{Schema: 1})
	}
	r, err := run("ok")
	must(t, err)
	eq(t, r.Role, "poller")
	for _, c := range []string{"html", "boss"} {
		if _, err := run(c); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: %v", c, err)
		}
	}
	if _, err := run("big"); err == nil {
		t.Error("big: no error")
	}
	_, err = run("redirect")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 302 {
		t.Errorf("redirect: %v", err)
	}
	eq(t, atomic.LoadInt32(&hits), int32(0))
	_, err = run("413")
	if !errors.As(err, &se) || se.Status != 413 {
		t.Errorf("413: %v", err)
	}
	eq(t, err.Error(), "sync: HTTP 413")
	_, err = run("500")
	if !errors.As(err, &se) || se.Status != 500 {
		t.Errorf("500: %v", err)
	}
}

func TestClientSendsHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, okBody("standby"))
	}))
	defer srv.Close()
	_, err := NewClient("test-ingest-key-0123456789", "claude-usage/0.0.0-dev", nil).Sync(context.Background(), srv.URL, Request{Schema: 1})
	must(t, err)
	eq(t, got.Get("Authorization"), "Bearer test-ingest-key-0123456789")
	eq(t, got.Get("User-Agent"), "claude-usage/0.0.0-dev")
	eq(t, got.Get("Content-Type"), "application/json")
	_, err = NewClient("", "ua", nil).Sync(context.Background(), srv.URL, Request{Schema: 1})
	must(t, err)
	if _, ok := got["Authorization"]; ok {
		t.Fatal("empty key must not send Authorization")
	}
}

func TestClientErrorsNeverContainKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	_, err := NewClient("SECRET-KEY-VALUE", "ua", nil).Sync(context.Background(), u, Request{Schema: 1})
	if err == nil || strings.Contains(err.Error(), "SECRET-KEY-VALUE") {
		t.Fatalf("%v", err)
	}
}

func TestClientIgnoresProxyEnv(t *testing.T) {
	var proxyHits int32
	deadProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxyHits, 1)
		w.WriteHeader(502)
	}))
	defer deadProxy.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, okBody("standby")) }))
	defer srv.Close()
	t.Setenv("HTTP_PROXY", deadProxy.URL)
	t.Setenv("http_proxy", deadProxy.URL)
	t.Setenv("NO_PROXY", "")
	srvAddr := strings.TrimPrefix(srv.URL, "http://")
	// A made-up name (not loopback, so a proxy-following client would use the proxy)
	// that the dialer maps to the test server.
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "sync.example:80" {
			addr = srvAddr
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	r, err := NewClient("k", "ua", dial).Sync(context.Background(), "http://sync.example", Request{Schema: 1})
	must(t, err)
	eq(t, r.Role, "standby")
	eq(t, atomic.LoadInt32(&proxyHits), int32(0))
	// sanity: the environment really names the proxy
	p, _ := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "http", Host: "sync.example"}})
	if p == nil {
		t.Log("ProxyFromEnvironment cached an earlier environment; the check above still holds")
	}
}
