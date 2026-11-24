package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	key   = "ci-ingest-key-0123456789"
	token = "fake-access-token-0123456789"
)

func newTest(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	files, scripts := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "SHA256SUMS"), []byte("x  ai-usage-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "install-ci.sh"), []byte("BASE='@BASE@'\nKEY='@KEY@'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newServer(Config{Key: key, Token: token, Files: files, Scripts: scripts}))
	t.Cleanup(srv.Close)
	return srv, files
}

func post(t *testing.T, srv *httptest.Server, auth, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/sync", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	res, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

type statsDoc struct {
	Syncs      int `json:"syncs"`
	BadKey     int `json:"bad_key"`
	TokenLeaks int `json:"token_leaks"`
	Devices    map[string]struct {
		Syncs         int    `json:"syncs"`
		LastMode      string `json:"last_mode"`
		LastVersion   string `json:"last_version"`
		LastAuthState string `json:"last_auth_state"`
		LastEmail     string `json:"last_email"`
		WantFresh     int    `json:"want_fresh"`
	} `json:"devices"`
}

func stats(t *testing.T, srv *httptest.Server) (statsDoc, string) {
	t.Helper()
	_, body := get(t, srv, "/stats")
	var s statsDoc
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return s, body
}

func syncBody(device, mode string, wantFresh bool) string {
	b, _ := json.Marshal(map[string]any{
		"schema": 1, "client": map[string]any{"version": "0.0.0-ci", "os": "linux", "mode": mode},
		"device": map[string]any{"id": device, "hostname": "TEST-PC-01"}, "auth": map[string]any{"state": "ok"},
		"account": map[string]any{"uuid": "acc-ci-1", "email": "user-a@example.com"}, "results": []any{}, "want_fresh": wantFresh,
	})
	return string(b)
}

func TestSyncRequiresKey(t *testing.T) {
	srv, _ := newTest(t)
	for _, auth := range []string{"", "Bearer wrong-key-0123456789"} {
		code, m := post(t, srv, auth, syncBody("d1", "tray", false))
		if code != 401 || m["error"] != "bad_ingest_key" {
			t.Fatalf("%q: %d %v", auth, code, m)
		}
	}
	s, _ := stats(t, srv)
	if s.BadKey != 2 || s.Syncs != 0 {
		t.Fatalf("%+v", s)
	}
	code, m := post(t, srv, "Bearer "+key, syncBody("d1", "tray", false))
	if code != 200 || m["role"] != "standby" || m["fetch_now"] != false || m["next_sync_seconds"] != 15.0 {
		t.Fatalf("%d %v", code, m)
	}
	limits := m["latest"].(map[string]any)["usage"].(map[string]any)["limits"].([]any)
	if limits[0].(map[string]any)["percent"] != 42.0 || limits[1].(map[string]any)["percent"] != 40.0 {
		t.Fatalf("%v", limits)
	}
	if m["server_urls"].([]any)[0] != srv.URL {
		t.Fatalf("server_urls %v", m["server_urls"])
	}
	if code, body := get(t, srv, "/healthz"); code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestStatsPerDevice(t *testing.T) {
	srv, _ := newTest(t)
	post(t, srv, "Bearer "+key, syncBody("d1", "tray", false))
	post(t, srv, "Bearer "+key, syncBody("d1", "tray", true))
	post(t, srv, "Bearer "+key, syncBody("d2", "headless", false))
	s, _ := stats(t, srv)
	d1 := s.Devices["d1"]
	if s.Syncs != 3 || d1.Syncs != 2 || d1.LastMode != "tray" || d1.LastVersion != "0.0.0-ci" || d1.LastAuthState != "ok" ||
		d1.LastEmail != "user-a@example.com" || d1.WantFresh != 1 || s.Devices["d2"].LastMode != "headless" {
		t.Fatalf("%+v", s)
	}
}

func TestTokenLeakCounted(t *testing.T) {
	srv, _ := newTest(t)
	post(t, srv, "Bearer "+key, `{"device":{"id":"d1"},"note":"`+token+`"}`)
	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Header.Set("X-Anything", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	s, body := stats(t, srv)
	if s.TokenLeaks != 2 {
		t.Fatalf("%+v", s)
	}
	if strings.Contains(body, token) || strings.Contains(body, key) {
		t.Fatal("stats shows a secret")
	}
}

func TestScriptSubstitution(t *testing.T) {
	srv, _ := newTest(t)
	code, body := get(t, srv, "/files/install-ci.sh")
	if code != 200 || !strings.Contains(body, "BASE='"+srv.URL+"'") || !strings.Contains(body, key) || strings.Contains(body, "@BASE@") {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := get(t, srv, "/files/SHA256SUMS"); code != 200 || !strings.Contains(body, "ai-usage-linux-amd64") {
		t.Fatalf("%d %s", code, body)
	}
	for _, p := range []string{"/files/../main_test.go", "/files/nothing", "/files/"} {
		if code, _ := get(t, srv, p); code != 404 {
			t.Fatalf("%s: %d", p, code)
		}
	}
}

// The CI tray check changes the weekly percentage to see the tray follow.
func TestWeeklyCanBeChanged(t *testing.T) {
	srv, _ := newTest(t)
	weekly := func() any {
		t.Helper()
		_, m := post(t, srv, "Bearer "+key, syncBody("d1", "tray", false))
		return m["latest"].(map[string]any)["usage"].(map[string]any)["limits"].([]any)[0].(map[string]any)["percent"]
	}
	setWeekly := func(v string) int {
		t.Helper()
		res, err := http.Post(srv.URL+"/weekly?percent="+v, "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if w := weekly(); w != 42.0 {
		t.Fatalf("default %v", w)
	}
	if code := setWeekly("57"); code != 204 {
		t.Fatalf("set: %d", code)
	}
	if w := weekly(); w != 57.0 {
		t.Fatalf("after set %v", w)
	}
	for _, bad := range []string{"", "abc", "-1", "101", "1e9"} {
		if code := setWeekly(bad); code != 400 {
			t.Fatalf("%q: %d", bad, code)
		}
	}
	if w := weekly(); w != 57.0 {
		t.Fatalf("after bad values %v", w)
	}
	if code, _ := get(t, srv, "/weekly"); code != 405 {
		t.Fatalf("GET /weekly: %d", code)
	}
}

const v2body = `{"schema":2,"client":{"version":"0.0.0-ci","os":"linux","mode":"headless"},"device":{"id":"dev-2"},
 "machine":{"key":"0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a","source":"os"},"event":null,
 "providers":[{"provider":"claude","account":{"key":"acc-ci-1","email":"user-a@example.com"},"auth":{"state":"ok","token_expires_at":null},
  "parse_problem":null,"results":[],"want_fresh":true},
  {"provider":"codex","account":null,"auth":{"state":"not_signed_in","token_expires_at":null},"parse_problem":null,"results":[],"want_fresh":true}]}`

// Schema 2: one standby block per provider, Claude's with today's fake
// reply; the device's Claude sign-in, e-mail, machine part and refreshes
// are recorded as for schema 1.
func TestSchema2Sync(t *testing.T) {
	srv, _ := newTest(t)
	code, m := post(t, srv, "Bearer "+key, v2body)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	ps, _ := m["providers"].([]any)
	if len(ps) != 2 {
		t.Fatalf("%v", m)
	}
	c := ps[0].(map[string]any)
	if c["provider"] != "claude" || c["role"] != "standby" || c["latest"] == nil || c["peers"].(map[string]any)["pcs"] == nil {
		t.Fatalf("%v", c)
	}
	if x := ps[1].(map[string]any); x["provider"] != "codex" || x["latest"] != nil {
		t.Fatalf("%v", x)
	}
	s, _ := stats(t, srv)
	d := s.Devices["dev-2"]
	if d.Syncs != 1 || d.LastAuthState != "ok" || d.LastEmail != "user-a@example.com" || d.WantFresh != 1 || d.LastMode != "headless" {
		t.Fatalf("%+v", d)
	}
	var raw map[string]map[string]map[string]any
	_, body := get(t, srv, "/stats")
	_ = json.Unmarshal([]byte(body), &raw)
	dev := raw["devices"]["dev-2"]
	if dev["last_schema"] != 2.0 || dev["machine"] != true || dev["uninstall_events"] != 0.0 {
		t.Fatalf("%v", dev)
	}
	if strings.Contains(body, "0a0a0a0a0a0a0a0a") {
		t.Fatal("the machine key is shown")
	}
	// the uninstall event is counted and answered with no blocks
	code, m = post(t, srv, "Bearer "+key, `{"schema":2,"client":{"version":"0.0.0-ci","os":"linux"},"device":{"id":"dev-2"},"machine":null,"event":"uninstall","providers":[]}`)
	if code != 200 || len(m["providers"].([]any)) != 0 {
		t.Fatalf("%d %v", code, m)
	}
	_, body = get(t, srv, "/stats")
	_ = json.Unmarshal([]byte(body), &raw)
	if raw["devices"]["dev-2"]["uninstall_events"] != 1.0 || raw["devices"]["dev-2"]["syncs"] != 2.0 {
		t.Fatalf("%v", raw["devices"]["dev-2"])
	}
}

// -schema1-only is a server from before schema 2: 400 unsupported_schema.
func TestSchema1Only(t *testing.T) {
	files, scripts := t.TempDir(), t.TempDir()
	srv := httptest.NewServer(newServer(Config{Key: key, Token: token, Files: files, Scripts: scripts, Schema1Only: true}))
	defer srv.Close()
	code, m := post(t, srv, "Bearer "+key, v2body)
	if code != 400 || m["error"] != "unsupported_schema" {
		t.Fatalf("%d %v", code, m)
	}
	code, _ = post(t, srv, "Bearer "+key, `{"schema":1,"client":{"version":"0.0.0-ci","mode":"headless"},"device":{"id":"dev-1"},"auth":{"state":"ok"},"results":[],"want_fresh":false}`)
	if code != 200 {
		t.Fatalf("schema 1: %d", code)
	}
}

// POST /hold?seconds=N makes syncs wait N seconds before they are answered
// (a server that takes a sync and does not answer in time), 0 ends it.
func TestSyncCanBeHeld(t *testing.T) {
	srv, _ := newTest(t)
	hold := func(v string) int {
		t.Helper()
		res, err := http.Post(srv.URL+"/hold?seconds="+v, "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := hold("1"); code != 204 {
		t.Fatalf("hold: %d", code)
	}
	start := time.Now()
	if code, _ := post(t, srv, "Bearer "+key, syncBody("d1", "tray", false)); code != 200 {
		t.Fatalf("held sync: %d", code)
	}
	if d := time.Since(start); d < time.Second {
		t.Fatalf("answered after %v", d)
	}
	// a client that gives up ends the wait
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/v1/sync", strings.NewReader(syncBody("d1", "tray", false)))
	req.Header.Set("Authorization", "Bearer "+key)
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("answered within the hold")
	}
	if code := hold("0"); code != 204 {
		t.Fatalf("release: %d", code)
	}
	start = time.Now()
	post(t, srv, "Bearer "+key, syncBody("d1", "tray", false))
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("still held: %v", d)
	}
	for _, bad := range []string{"", "x", "-1", "601"} {
		if code := hold(bad); code != 400 {
			t.Fatalf("%q: %d", bad, code)
		}
	}
}

const v2bothBody = `{"schema":2,"client":{"version":"0.0.0-ci","os":"linux","mode":"tray"},"device":{"id":"dev-3"},
 "machine":{"key":"0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b","source":"os"},"event":null,
 "providers":[{"provider":"claude","account":{"key":"acc-ci-1","email":"user-a@example.com"},"auth":{"state":"ok","token_expires_at":null},
  "parse_problem":null,"results":[],"want_fresh":false},
  {"provider":"codex","account":{"key":"codex:00000000-0000-4000-8000-000000000001:user-ci","email":"user-ci@example.com","plan_type":"plus"},
   "auth":{"state":"ok","token_expires_at":null},"parse_problem":null,"results":[],"want_fresh":false}]}`

// Plan Task 11: a signed-in Codex block gets a standby answer with a
// wham-shaped latest (5-hour 42 %, weekly 17 %); each provider's sign-in
// state and e-mail are recorded per device.
func TestSchema2CodexBlock(t *testing.T) {
	srv, _ := newTest(t)
	code, m := post(t, srv, "Bearer "+key, v2bothBody)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	x := m["providers"].([]any)[1].(map[string]any)
	if x["provider"] != "codex" || x["role"] != "standby" || x["fetch_now"] != false {
		t.Fatalf("%v", x)
	}
	l, _ := x["latest"].(map[string]any)
	if l == nil || l["source"] != "wham" {
		t.Fatalf("latest %v", x["latest"])
	}
	rl := l["usage"].(map[string]any)["rate_limit"].(map[string]any)
	p := rl["primary_window"].(map[string]any)
	s := rl["secondary_window"].(map[string]any)
	if p["used_percent"] != 42.0 || p["limit_window_seconds"] != 18000.0 || s["used_percent"] != 17.0 || s["limit_window_seconds"] != 604800.0 {
		t.Fatalf("%v", rl)
	}
	var raw struct {
		Devices map[string]struct {
			Providers map[string]struct {
				LastAuthState string `json:"last_auth_state"`
				LastEmail     string `json:"last_email"`
			} `json:"providers"`
		} `json:"devices"`
	}
	_, body := get(t, srv, "/stats")
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	ps := raw.Devices["dev-3"].Providers
	if ps["codex"].LastAuthState != "ok" || ps["codex"].LastEmail != "user-ci@example.com" || ps["claude"].LastAuthState != "ok" {
		t.Fatalf("%+v", ps)
	}
}

// -token takes several fake values (comma-separated): any of them in a
// request is a leak.
func TestEveryFakeTokenIsWatched(t *testing.T) {
	files, scripts := t.TempDir(), t.TempDir()
	srv := httptest.NewServer(newServer(Config{Key: key, Token: token + ",fake-codex-sig-ci-0123456789", Files: files, Scripts: scripts}))
	defer srv.Close()
	post(t, srv, "Bearer "+key, strings.Replace(v2bothBody, "user-ci@example.com", "x.fake-codex-sig-ci-0123456789", 1))
	post(t, srv, "Bearer "+key, v2bothBody)
	s, _ := stats(t, srv)
	if s.TokenLeaks != 1 {
		t.Fatalf("leaks %d", s.TokenLeaks)
	}
}
