package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

type vector struct {
	Name      string `json:"name"`
	IngestKey string `json:"ingest_key"`
	Steps     []struct {
		At      string `json:"at"`
		Request struct {
			Headers map[string]string `json:"headers"`
			Body    json.RawMessage   `json:"body"`
		} `json:"request"`
		Response struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"response"`
	} `json:"steps"`
}

const vectorDir = "../testdata/vectors"

func vectorFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(vectorDir, "*.json"))
	must(t, err)
	sort.Strings(files)
	if len(files) != 9 {
		t.Fatalf("%d vector files", len(files))
	}
	return files
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	var out []vector
	for _, f := range vectorFiles(t) {
		b, err := os.ReadFile(f)
		must(t, err)
		var v vector
		must(t, json.Unmarshal(b, &v))
		if len(v.Steps) == 0 {
			t.Fatalf("%s: no steps", f)
		}
		out = append(out, v)
	}
	return out
}

func generic(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	must(t, json.Unmarshal(b, &v))
	return v
}

// subset reports whether every key in want is present in got with an equal
// (recursively subset) value. Arrays and scalars must match exactly.
func subset(want, got any) bool {
	wm, ok := want.(map[string]any)
	if !ok {
		return reflect.DeepEqual(want, got)
	}
	gm, ok := got.(map[string]any)
	if !ok {
		return false
	}
	for k, wv := range wm {
		gv, ok := gm[k]
		if !ok || !subset(wv, gv) {
			return false
		}
	}
	return true
}

func TestVectorsArePublicSafe(t *testing.T) {
	at := regexp.MustCompile(`[A-Za-z0-9._%+-]*@[A-Za-z0-9.-]*`)
	ipv4 := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	for _, f := range vectorFiles(t) {
		b, err := os.ReadFile(f)
		must(t, err)
		for i, c := range b {
			if c >= 0x80 {
				t.Fatalf("%s: non-ASCII byte at %d", f, i)
			}
		}
		for _, m := range at.FindAllString(string(b), -1) {
			if !strings.HasSuffix(m, "@example.com") {
				t.Errorf("%s: address %q", f, m)
			}
		}
		for _, m := range ipv4.FindAllString(string(b), -1) {
			if !strings.HasPrefix(m, "192.0.2.") {
				t.Errorf("%s: IPv4 %q", f, m)
			}
		}
	}
}

func TestVectorRequestsRoundTrip(t *testing.T) {
	for _, v := range loadVectors(t) {
		for i, s := range v.Steps {
			var req Request
			must(t, json.Unmarshal(s.Request.Body, &req))
			b, err := json.Marshal(req)
			must(t, err)
			if !reflect.DeepEqual(generic(t, b), generic(t, s.Request.Body)) {
				t.Errorf("%s step %d:\n got %s\nwant %s", v.Name, i, b, s.Request.Body)
			}
		}
	}
}

func TestVectorResponsesDecode(t *testing.T) {
	n := 0
	for _, v := range loadVectors(t) {
		for i, s := range v.Steps {
			if s.Response.Status != 200 {
				continue
			}
			n++
			var resp Response
			must(t, json.Unmarshal(s.Response.Body, &resp))
			b, _ := json.Marshal(resp)
			if !subset(generic(t, s.Response.Body), generic(t, b)) {
				t.Errorf("%s step %d:\n decoded %s\n vector %s", v.Name, i, b, s.Response.Body)
			}
		}
	}
	if n == 0 {
		t.Fatal("no 200 steps")
	}
}

// fullResponse completes a vector's (possibly partial) expected response
// body with neutral defaults, so a fake server can send it.
func fullResponse(t *testing.T, partial json.RawMessage) []byte {
	t.Helper()
	m := map[string]any{
		"role": "standby", "fetch_now": false, "next_sync_seconds": 60, "latest": nil,
		"peers": map[string]any{"devices": 1, "poller_hostname": nil}, "server_urls": []any{},
		"client_latest_version": nil,
	}
	for k, v := range generic(t, partial).(map[string]any) {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func TestClientReplaysVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			var mu sync.Mutex
			step := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				s := v.Steps[step]
				step++
				mu.Unlock()
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sync" {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				if got, want := r.Header.Get("Authorization"), s.Request.Headers["Authorization"]; got != want {
					t.Errorf("Authorization %q, want %q", got, want)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type %q", ct)
				}
				body, _ := io.ReadAll(r.Body)
				if !reflect.DeepEqual(generic(t, body), generic(t, s.Request.Body)) {
					t.Errorf("body\n got %s\nwant %s", body, s.Request.Body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(s.Response.Status)
				if s.Response.Status == 200 {
					_, _ = w.Write(fullResponse(t, s.Response.Body))
				} else {
					_, _ = w.Write(s.Response.Body)
				}
			}))
			defer srv.Close()
			for i, s := range v.Steps {
				key := strings.TrimPrefix(s.Request.Headers["Authorization"], "Bearer ")
				var req Request
				must(t, json.Unmarshal(s.Request.Body, &req))
				resp, err := NewClient(key, "ai-usage/test", nil).Sync(context.Background(), srv.URL, req)
				switch s.Response.Status {
				case 200:
					must(t, err)
					b, _ := json.Marshal(resp)
					if !subset(generic(t, s.Response.Body), generic(t, b)) {
						t.Errorf("step %d: response %s, vector %s", i, b, s.Response.Body)
					}
				case 401:
					if !errors.Is(err, ErrBadIngestKey) {
						t.Errorf("step %d: %v", i, err)
					}
				case 400:
					if !errors.Is(err, ErrUnsupportedSchema) {
						t.Errorf("step %d: %v", i, err)
					}
				default:
					t.Fatalf("step %d: unexpected status %d in vector", i, s.Response.Status)
				}
			}
			eq(t, step, len(v.Steps))
		})
	}
}
