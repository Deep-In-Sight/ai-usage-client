package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseInstallerConfig(t *testing.T) {
	c, err := Parse([]byte("\ufeff" + `{"server_urls":["http://claude-usage.local/","http://192.0.2.10:8700"],"ingest_key":"k-123","extra":1}`))
	must(t, err)
	eq(t, c.ServerURLs, []string{"http://claude-usage.local", "http://192.0.2.10:8700"})
	eq(t, c.IngestKey, "k-123")
	eq(t, c.ClaudeConfigDir, "")
}

func TestValidateRejects(t *testing.T) { // each must fail, and no error text contains the key
	long := strings.Repeat("a", 257)
	elevenURLs := `["http://a1","http://a2","http://a3","http://a4","http://a5","http://a6","http://a7","http://a8","http://a9","http://a10","http://a11"]`
	for _, s := range []string{
		`{"server_urls":[],"ingest_key":"k"}`,
		`{"server_urls":["ftp://x"],"ingest_key":"k"}`,
		`{"server_urls":["http://x/a?b"],"ingest_key":"k"}`,
		`{"server_urls":["http://x"],"ingest_key":""}`,
		`{"server_urls":["http://x"],"ingest_key":"has space-SECRETVALUE"}`,
		`{"server_urls":["http://x"],"ingest_key":"k"} trailing`,
		`{"server_urls":["http://u:p@x"],"ingest_key":"k"}`,
		`{"server_urls":["http://x#f"],"ingest_key":"k"}`,
		`{"server_urls":["http://"],"ingest_key":"k"}`,
		`{"server_urls":` + elevenURLs + `,"ingest_key":"k"}`,
		`{"server_urls":["http://x"],"ingest_key":"` + long + `"}`,
		`{"server_urls":["http://x"],"ingest_key":"tab\tSECRETVALUE"}`,
		`{"server_urls":["http://x"],"ingest_key":"k","claude_config_dir":"relative/dir"}`,
		`[]`,
		`not json SECRETVALUE`,
	} {
		_, err := Parse([]byte(s))
		if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://claude-usage.local/": "http://claude-usage.local",
		"HTTP://192.0.2.10:8700":     "http://192.0.2.10:8700",
		" https://x.example ":        "https://x.example",
	} {
		got, err := NormalizeURL(in)
		must(t, err)
		eq(t, got, want)
	}
}

func TestSaveIsPrivateAndRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	p := filepath.Join(dir, "config.json")
	abs := t.TempDir()
	c := Config{ServerURLs: []string{"http://claude-usage.local", "http://192.0.2.10:8700"}, IngestKey: "k-123", ClaudeConfigDir: abs}
	must(t, Save(p, c))
	got, err := Load(p)
	must(t, err)
	eq(t, got, c)
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		eq(t, st.Mode().Perm(), os.FileMode(0o600))
		st, _ = os.Stat(dir)
		eq(t, st.Mode().Perm(), os.FileMode(0o700))
	}
	// overwrite keeps working
	c.IngestKey = "k-456"
	must(t, Save(p, c))
	got, err = Load(p)
	must(t, err)
	eq(t, got.IngestKey, "k-456")
}

func TestDeviceIDStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "device-id")
	a, err := DeviceID(p)
	must(t, err)
	b, err := DeviceID(p)
	must(t, err)
	eq(t, a, b)
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(a) {
		t.Fatal(a)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		eq(t, st.Mode().Perm(), os.FileMode(0o600))
	}
	// an empty file gets a new id
	must(t, os.WriteFile(p, []byte("\n"), 0o600))
	c, err := DeviceID(p)
	must(t, err)
	if c == "" || c == a {
		t.Fatalf("empty file: %q", c)
	}
}

func TestStateMissingOrCorruptIsZero(t *testing.T) {
	dir := t.TempDir()
	eq(t, LoadState(filepath.Join(dir, "none.json")), State{})
	bad := filepath.Join(dir, "bad.json")
	must(t, os.WriteFile(bad, []byte("{not json"), 0o600))
	eq(t, LoadState(bad), State{})
	p := filepath.Join(dir, "state.json")
	s := State{LastGoodURL: "http://192.0.2.10:8700", ServerURLs: []string{"http://claude-usage.local"}, BatchLimit: 500}
	must(t, SaveState(p, s))
	eq(t, LoadState(p), s)
}
