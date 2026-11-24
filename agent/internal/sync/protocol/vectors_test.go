package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if len(files) != 20 {
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
// (recursively subset) value. Arrays and scalars must match exactly; the
// string "<any>" in want matches any value.
func subset(want, got any) bool {
	if want == "<any>" {
		return true
	}
	wm, ok := want.(map[string]any)
	if !ok {
		wa, ok := want.([]any)
		ga, ok2 := got.([]any)
		if ok && ok2 {
			if len(wa) != len(ga) {
				return false
			}
			for i := range wa {
				if !subset(wa[i], ga[i]) {
					return false
				}
			}
			return true
		}
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

// sameBody compares a request body with a vector's: equal, except that a
// key the vector leaves out must be null here (the server reads a missing
// optional part as null), and "<any>" matches any value.
func sameBody(want, got any) bool {
	if want == "<any>" {
		return true
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			if !sameBody(wv, g[k]) {
				return false
			}
		}
		for k, gv := range g {
			if _, ok := w[k]; !ok && gv != nil {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !sameBody(w[i], g[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}

// schemaOf is a step's request schema.
func schemaOf(t *testing.T, body json.RawMessage) int {
	t.Helper()
	var v struct{ Schema int }
	must(t, json.Unmarshal(body, &v))
	return v.Schema
}

// malformed are the vector steps whose request is wrong on purpose (the
// server's handling of a bad client part); this agent never sends them.
var malformed = map[string]bool{"18-unreadable-parts-degrade/0": true, "20-degraded-block-keeps-results/0": true}

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
	n2 := 0
	for _, v := range loadVectors(t) {
		for i, s := range v.Steps {
			var b []byte
			var err error
			if schemaOf(t, s.Request.Body) == SchemaV2 {
				if malformed[fmt.Sprintf("%s/%d", v.Name, i)] {
					continue
				}
				var req RequestV2
				must(t, json.Unmarshal(s.Request.Body, &req))
				b, err = json.Marshal(req)
				n2++
			} else {
				var req Request
				must(t, json.Unmarshal(s.Request.Body, &req))
				b, err = json.Marshal(req)
			}
			must(t, err)
			if !sameBody(generic(t, s.Request.Body), generic(t, b)) {
				t.Errorf("%s step %d:\n got %s\nwant %s", v.Name, i, b, s.Request.Body)
			}
		}
	}
	if n2 < 20 {
		t.Fatalf("only %d schema 2 steps", n2)
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
			var b []byte
			if schemaOf(t, s.Request.Body) == SchemaV2 {
				var resp ResponseV2
				must(t, json.Unmarshal(s.Response.Body, &resp))
				b, _ = json.Marshal(resp)
			} else {
				var resp Response
				must(t, json.Unmarshal(s.Response.Body, &resp))
				b, _ = json.Marshal(resp)
			}
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
func fullResponse(t *testing.T, partial json.RawMessage, schema int) []byte {
	t.Helper()
	m := map[string]any{
		"role": "standby", "fetch_now": false, "next_sync_seconds": 60, "latest": nil,
		"peers": map[string]any{"devices": 1, "poller_hostname": nil}, "server_urls": []any{},
		"client_latest_version": nil,
	}
	if schema == SchemaV2 {
		m = map[string]any{"providers": []any{}, "next_sync_seconds": 60, "server_urls": []any{}, "client_latest_version": nil}
	}
	for k, v := range generic(t, partial).(map[string]any) {
		m[k] = v
	}
	if ps, ok := m["providers"].([]any); ok {
		for _, p := range ps {
			b := p.(map[string]any)
			for k, v := range map[string]any{"role": "standby", "fetch_now": false, "latest": nil,
				"peers": map[string]any{"devices": 1, "pcs": 1, "poller_hostname": nil}} {
				if _, ok := b[k]; !ok {
					b[k] = v
				}
			}
			if l, ok := b["latest"].(map[string]any); ok && l["usage"] == "<any>" {
				l["usage"] = map[string]any{}
			}
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func TestClientReplaysVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			var mu sync.Mutex
			step := 0
			var steps []int // the vector steps this agent can send
			for i := range v.Steps {
				if !malformed[fmt.Sprintf("%s/%d", v.Name, i)] {
					steps = append(steps, i)
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				s := v.Steps[steps[step]]
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
				if !sameBody(generic(t, s.Request.Body), generic(t, body)) {
					t.Errorf("body\n got %s\nwant %s", body, s.Request.Body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(s.Response.Status)
				if s.Response.Status == 200 {
					_, _ = w.Write(fullResponse(t, s.Response.Body, schemaOf(t, s.Request.Body)))
				} else {
					_, _ = w.Write(s.Response.Body)
				}
			}))
			defer srv.Close()
			for _, i := range steps {
				s := v.Steps[i]
				key := strings.TrimPrefix(s.Request.Headers["Authorization"], "Bearer ")
				cl := NewClient(key, "ai-usage/test", nil)
				var b []byte
				var err error
				if schemaOf(t, s.Request.Body) == SchemaV2 {
					var req RequestV2
					must(t, json.Unmarshal(s.Request.Body, &req))
					var resp ResponseV2
					resp, err = cl.SyncV2(context.Background(), srv.URL, req)
					b, _ = json.Marshal(resp)
				} else {
					var req Request
					must(t, json.Unmarshal(s.Request.Body, &req))
					var resp Response
					resp, err = cl.Sync(context.Background(), srv.URL, req)
					b, _ = json.Marshal(resp)
				}
				switch s.Response.Status {
				case 200:
					must(t, err)
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
			eq(t, step, len(steps))
		})
	}
}
