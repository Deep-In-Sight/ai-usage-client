package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The certificate is valid for the vendors' names under the made-up
// authority; the usage address answers as the mode says; everything else
// is 404; stats count requests and whether they had a token, never its
// value.
func TestFakeVendor(t *testing.T) {
	cert, caPEM, err := certificates()
	if err != nil {
		t.Fatal(err)
	}
	v := newVendor("403")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(v.serve))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	admin := httptest.NewServer(v.admin())
	defer admin.Close()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("no CA in the PEM")
	}
	addr := srv.Listener.Addr().String()
	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	get := func(url string, token bool) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if token {
			req.Header.Set("Authorization", "Bearer fake-token-for-the-test")
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := get("https://chatgpt.com/backend-api/wham/usage", true)
	if code != 403 || body != forbiddenReply {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := get("https://api.anthropic.com/api/oauth/usage", false); code != 404 {
		t.Fatalf("anthropic: %d", code)
	}
	res, err := http.Post(admin.URL+"/mode?m=nulls", "", nil)
	if err != nil || res.StatusCode != 204 {
		t.Fatalf("mode: %v %v", res, err)
	}
	if code, body := get("https://chatgpt.com/backend-api/wham/usage", true); code != 200 || body != nullsReply {
		t.Fatalf("%d %s", code, body)
	}
	res, err = http.Get(admin.URL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), "fake-token") {
		t.Fatal("stats show a token")
	}
	var st struct {
		Requests map[string]int `json:"requests"`
		Auth     map[string]int `json:"with_authorization"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.Requests["chatgpt.com GET /backend-api/wham/usage"] != 2 || st.Auth["chatgpt.com GET /backend-api/wham/usage"] != 2 ||
		st.Requests["api.anthropic.com GET /api/oauth/usage"] != 1 || st.Auth["api.anthropic.com GET /api/oauth/usage"] != 0 {
		t.Fatalf("stats %s", b)
	}
	// the certificate does not cover other names, and the authority may
	// only vouch for the vendors' names (re-review R-2)
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	if leaf.VerifyHostname("example.com") == nil {
		t.Fatal("covers example.com")
	}
	ca, _ := x509.ParseCertificate(cert.Certificate[1])
	if !ca.PermittedDNSDomainsCritical || strings.Join(ca.PermittedDNSDomains, " ") != strings.Join(Hosts, " ") {
		t.Fatalf("CA name constraints %v %v", ca.PermittedDNSDomainsCritical, ca.PermittedDNSDomains)
	}
}
