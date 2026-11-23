package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	key   = "ci-ingest-key-0123456789"
	token = "fake-access-token-0123456789"
)

func newTest(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	files, scripts := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "SHA256SUMS"), []byte("x  claude-usage-linux-amd64\n"), 0o644); err != nil {
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
	if code, body := get(t, srv, "/files/SHA256SUMS"); code != 200 || !strings.Contains(body, "claude-usage-linux-amd64") {
		t.Fatalf("%d %s", code, body)
	}
	for _, p := range []string{"/files/../main_test.go", "/files/nothing", "/files/"} {
		if code, _ := get(t, srv, p); code != 404 {
			t.Fatalf("%s: %d", p, code)
		}
	}
}
