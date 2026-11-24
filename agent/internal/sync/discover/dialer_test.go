package discover

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"
	"time"
)

func get(t *testing.T, d func(ctx context.Context, network, addr string) (net.Conn, error), u string) *http.Response {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: d}}
	resp, err := c.Get(u)
	must(t, err)
	resp.Body.Close()
	return resp
}

func TestDialerResolvesLocalNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port := u.Port()
	lookups := 0
	d := NewDialer(func(_ context.Context, name string) ([]net.IP, error) {
		lookups++
		if name != "ai-usage.local" {
			t.Errorf("lookup %q", name)
		}
		// the first address does not answer; the dialer must try the next
		return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("127.0.0.1")}, nil
	})
	// 192.0.2.1 is unroutable: give the dialer a short timeout for the test
	dialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dialTimeout = defaultDialTimeout })
	resp := get(t, d, "http://ai-usage.local:"+port+"/healthz")
	eq(t, resp.StatusCode, 200)
	resp = get(t, d, "http://AI-USAGE.local.:"+port+"/healthz")
	eq(t, resp.StatusCode, 200)
	eq(t, lookups, 1) // cached for 60 s

	// other names use the normal dialer, not the lookup
	resp = get(t, d, srv.URL+"/healthz")
	eq(t, resp.StatusCode, 200)
	eq(t, lookups, 1)
}

func TestDialerLookupFailure(t *testing.T) {
	d := NewDialer(func(context.Context, string) ([]net.IP, error) { return nil, nil })
	_, err := d(context.Background(), "tcp", "ai-usage.local:80")
	if err == nil {
		t.Fatal("no addresses should fail")
	}
	d = NewDialer(func(context.Context, string) ([]net.IP, error) { return nil, errors.New("boom") })
	if _, err := d(context.Background(), "tcp", "ai-usage.local:80"); err == nil {
		t.Fatal("lookup error should fail")
	}
}

func TestPlatformDialer(t *testing.T) {
	if (PlatformDialer() != nil) != (runtime.GOOS == "linux") {
		t.Fatal("PlatformDialer: own mDNS only on linux")
	}
}
