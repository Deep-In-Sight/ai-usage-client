package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Nil lists go out as [], a missing machine part and no event as null.
func TestRequestV2Marshal(t *testing.T) {
	b, err := json.Marshal(RequestV2{Schema: SchemaV2, Providers: []ProviderBlock{{Provider: "claude", Auth: AuthV2{State: "not_signed_in"}}}})
	must(t, err)
	s := string(b)
	for _, want := range []string{`"schema":2`, `"machine":null`, `"event":null`, `"account":null`, `"results":[]`,
		`"token_expires_at":null`, `"parse_problem":null`, `"want_fresh":false`} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %s in %s", want, s)
		}
	}
	b, _ = json.Marshal(RequestV2{Schema: SchemaV2})
	if !strings.Contains(string(b), `"providers":[]`) {
		t.Fatal(string(b))
	}
	ev := EventUninstall
	b, _ = json.Marshal(RequestV2{Schema: SchemaV2, Machine: &MachineInfo{Key: "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a", Source: "os"}, Event: &ev})
	if !strings.Contains(string(b), `"machine":{"key":"0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a","source":"os"}`) || !strings.Contains(string(b), `"event":"uninstall"`) {
		t.Fatal(string(b))
	}
	secs := 30
	b, _ = json.Marshal(ResultV2{AccountKey: "codex:a:b", HTTPStatus: 429, Source: "wham", RetryAfterSeconds: &secs})
	for _, want := range []string{`"account_key":"codex:a:b"`, `"usage":null`, `"source":"wham"`, `"cli_version":null`, `"retry_after_seconds":30`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("no %s in %s", want, b)
		}
	}
}

// HTTP 400 {"error":"unsupported_schema"} is the server saying it does not
// speak schema 2 (a v5 server); a 200 without "providers" is a server that
// ignored the schema, which is the same. Any other 400 is just an error.
func TestUnsupportedSchemaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Case") {
		case "unsupported":
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"unsupported_schema"}`)
		case "other400":
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"bad_request"}`)
		case "schema1":
			fmt.Fprint(w, okBody("standby"))
		case "badrole":
			fmt.Fprint(w, `{"providers":[{"provider":"claude","role":"boss","fetch_now":false,"latest":null,"peers":{"devices":1,"pcs":1}}],"next_sync_seconds":60,"server_urls":[]}`)
		default:
			fmt.Fprint(w, `{"providers":[{"provider":"claude","role":"poller","fetch_now":true,"latest":null,"peers":{"devices":1,"pcs":1,"poller_hostname":"TEST-PC-A"},"problem":null}],"next_sync_seconds":60,"server_urls":[],"client_latest_version":null}`)
		}
	}))
	defer srv.Close()
	run := func(c string) (ResponseV2, error) {
		cl := NewClient("k", "ua", nil)
		cl.extraHeader = func(h http.Header) { h.Set("X-Case", c) }
		return cl.SyncV2(context.Background(), srv.URL, RequestV2{Schema: SchemaV2})
	}
	r, err := run("ok")
	must(t, err)
	eq(t, len(r.Providers), 1)
	eq(t, r.Providers[0].Role, "poller")
	eq(t, *r.Providers[0].Peers.PollerHostname, "TEST-PC-A")
	if _, err := run("unsupported"); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("unsupported: %v", err)
	}
	// a 200 without "providers" (a captive portal's or proxy's JSON, a
	// server that ignored the schema) is no answer, not a reason for an
	// hour of schema 1: only the server's own 400 body is (review m-R)
	if _, err := run("schema1"); !errors.Is(err, ErrBadResponse) || errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("200 without providers: %v", err)
	}
	var se *StatusError
	if _, err := run("other400"); !errors.As(err, &se) || se.Status != 400 {
		t.Errorf("other 400: %v", err)
	}
	if _, err := run("badrole"); !errors.Is(err, ErrBadResponse) {
		t.Errorf("bad role: %v", err)
	}
}
