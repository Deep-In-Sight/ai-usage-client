package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	initLine = `{"id":0,"method":"initialize","params":{"clientInfo":{"name":"ai_usage","title":"AI Usage","version":"0.0.0-test"}}}`
	readLine = `{"id":1,"method":"account/rateLimits/read","params":{"excludeResetCreditDetails":true}}`
)

func answers(t *testing.T, out string) map[int]map[string]json.RawMessage {
	t.Helper()
	got := map[int]map[string]json.RawMessage{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		var id int
		if raw, ok := m["id"]; ok {
			_ = json.Unmarshal(raw, &id)
			got[id] = m
		}
	}
	return got
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if run([]string{"--version"}, strings.NewReader(""), &out) != 0 || out.String() != "codex-cli 9.9.9\n" {
		t.Fatalf("%q", out.String())
	}
}

func TestAnswersTheRateLimitRead(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(initLine + "\n" + `{"method":"initialized"}` + "\n" + readLine + "\n")
	if code := run([]string{"app-server", "--listen", "stdio://"}, in, &out); code != 0 {
		t.Fatal(code)
	}
	a := answers(t, out.String())
	if !strings.Contains(string(a[0]["result"]), "codex_cli_rs/9.9.9") {
		t.Fatalf("initialize: %s", a[0]["result"])
	}
	var res map[string]any
	if err := json.Unmarshal(a[1]["result"], &res); err != nil || res["rateLimits"] == nil {
		t.Fatalf("read: %s", a[1]["result"])
	}
}

func TestRefusesOtherArguments(t *testing.T) {
	if run([]string{"app-server"}, strings.NewReader(""), &bytes.Buffer{}) != 2 {
		t.Fatal("ran with other arguments")
	}
}
