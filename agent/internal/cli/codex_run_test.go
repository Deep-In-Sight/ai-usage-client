package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

// Invented values only: none of these is a real token or account.
const (
	codexFakeAccess  = "fake-codex-access-EEEEEEEEEEEEEEEE"
	codexFakeRefresh = "fake-codex-refresh-FFFFFFFFFFFFFFFF"
	codexFakeID      = "fake-codex-idtoken-GGGGGGGGGGGGGGGG"
	codexFakeAccount = "00000000-0000-4000-8000-000000000003"
	codexFakeUser    = "user-FakeRunTest01"
)

func codexJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

// codexSignIn writes a Codex sign-in into home's ~/.codex; it returns the
// access token as written (every token string must stay on this PC).
func codexSignIn(t *testing.T, home string) []string {
	t.Helper()
	access := codexJWT(map[string]any{"exp": time.Now().Add(9 * 24 * time.Hour).Unix(), "marker": codexFakeAccess})
	id := codexJWT(map[string]any{"email": "user-c@example.com", "marker": codexFakeID,
		"https://api.openai.com/auth": map[string]any{"chatgpt_user_id": codexFakeUser, "chatgpt_plan_type": "plus"}})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "last_refresh": time.Now().UTC().Format(time.RFC3339),
		"tokens": map[string]any{"access_token": access, "account_id": codexFakeAccount, "id_token": id, "refresh_token": codexFakeRefresh}})
	writeAt(t, filepath.Join(home, ".codex", "auth.json"), b)
	return []string{access, id, codexFakeRefresh, codexFakeAccess, codexFakeID}
}

func writeAt(t *testing.T, p string, b []byte) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	must(t, os.WriteFile(p, b, 0o600))
}

// fakeChatGPT stands in for chatgpt.com: the run's Codex transport dials
// it whatever the host.
type fakeChatGPT struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
	hosts []string
}

func newFakeChatGPT(t *testing.T) *fakeChatGPT {
	f := &fakeChatGPT{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.hosts = append(f.hosts, r.Host+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,`+
			`"primary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_after_seconds":600,"reset_at":1795508400},`+
			`"secondary_window":{"used_percent":17,"limit_window_seconds":604800,"reset_after_seconds":6000,"reset_at":1795603500}},`+
			`"credits":{"has_credits":false,"unlimited":false,"balance":"0"},"additional_rate_limits":[]}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeChatGPT) transport() http.RoundTripper {
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	addr := f.Listener.Addr().String()
	return &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"}}
}

func (f *fakeChatGPT) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...), append([]string(nil), f.hosts...)
}

// v2Server is a schema 2 sync server that asks this PC to fetch Codex
// once and keeps every request body as sent.
type v2Server struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newV2Server(t *testing.T) *v2Server {
	s := &v2Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(body))
		first := len(s.bodies) == 1
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"providers":[{"provider":"claude","role":"standby","fetch_now":false,"latest":null,"peers":{"devices":1,"pcs":1,"poller_hostname":null},"problem":null},`+
			`{"provider":"codex","role":"poller","fetch_now":%v,"latest":null,"peers":{"devices":1,"pcs":1,"poller_hostname":"TEST-PC-A"},"problem":null}],`+
			`"next_sync_seconds":15,"server_urls":[],"client_latest_version":null}`, first)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *v2Server) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// The Codex token goes to chatgpt.com in its Authorization header and
// nowhere else: not in any sync body, status.json, backlog.json,
// latest-codex.json or agent.log (design 6.3; plan Task 8).
func TestTokenNeverLeavesCodexPackage(t *testing.T) {
	srv := newV2Server(t)
	chat := newFakeChatGPT(t)
	te := newEnv(t)
	te.Clock = clock.Real{}
	must(t, config.Save(te.Dirs.File(paths.ConfigFile), mustParse(t, `{"server_urls":["`+srv.URL+`"],"ingest_key":"test-ingest-key-0123456789"}`)))
	te.Syncer = func(cfg config.Config, ua string) engine.Syncer {
		return onlyTo{base: srv.URL, next: protocol.NewClient(cfg.IngestKey, ua, nil)}
	}
	te.CodexTransport = chat.transport()
	secrets := codexSignIn(t, te.home)
	cancel, code := te.mainAsync("run", "--headless")
	deadline := time.Now().Add(15 * time.Second)
	for {
		bodies := srv.all()
		if len(bodies) >= 2 && strings.Contains(bodies[len(bodies)-1], `"source":"wham"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no Codex result reached the server: %d syncs", len(bodies))
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitStatus(t, te, func(s status.Snapshot) bool {
		r, ok := s.Providers[provider.Codex].Row("codex:weekly")
		return ok && r.Percent != nil && *r.Percent == 17
	})
	cancel()
	eq(t, waitCode(t, code), 0)

	auths, hosts := chat.seen()
	eq(t, len(auths), 1)
	eq(t, auths[0], "Bearer "+secrets[0])
	eq(t, hosts[0], "chatgpt.com/backend-api/wham/usage")
	for i, b := range srv.all() {
		for _, s := range secrets {
			if strings.Contains(b, s) {
				t.Fatalf("a token string is in sync body %d", i)
			}
		}
	}
	for _, f := range []string{paths.StatusFile, paths.BacklogFile, paths.LatestFileFor(provider.Codex), paths.LogFile, paths.StateFile} {
		b, err := os.ReadFile(te.Dirs.File(f))
		if err != nil {
			if f == paths.BacklogFile || f == paths.StateFile {
				continue
			}
			t.Fatalf("%s: %v", f, err)
		}
		for _, s := range secrets {
			if strings.Contains(string(b), s) {
				t.Fatalf("a token string is in %s", f)
			}
		}
	}
	// the server got the Codex block with the account, never the token
	last := srv.all()[len(srv.all())-1]
	for _, want := range []string{`"provider":"codex"`, `"key":"codex:` + codexFakeAccount + `:` + codexFakeUser + `"`, `"plan_type":"plus"`,
		`"state":"ok"`, `"http_status":200`} {
		if !strings.Contains(last, want) {
			t.Fatalf("%s not in the last sync: %s", want, last)
		}
	}
}

// A Codex sign-in on a PC without Claude: the Codex helper (uid 101) is
// started and the menu has the Codex section (plan Task 8).
func TestCodexAppearsInMenu(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	codexSignIn(t, te.home)
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	if h.id != provider.Codex || h.uid != 101 {
		t.Fatalf("helper %q uid %d", h.id, h.uid)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.HasPrefix(itemText(h.lastView(), "h.codex"), "Codex · user-c@… · ") {
		if time.Now().After(deadline) {
			t.Fatalf("no Codex section: %+v", h.lastView().Items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	contains(t, itemText(h.lastView(), "h.codex"), "Plus")
	eq(t, itemText(h.lastView(), "h.claude"), "") // Claude is not signed in: no section
	eq(t, itemText(h.lastView(), "p.claude"), "") // and no prompt: someone is signed in
	eq(t, h.lastView().Icon.Provider, string(provider.Codex))
	eq(t, te.helpers.count(), 1)
	cancel()
	eq(t, waitCode(t, code), 0)
	// the sync named the Codex account
	reqs := fs.v2requests()
	found := false
	for _, r := range reqs {
		for _, b := range r.Providers {
			if b.Provider == "codex" && b.Account != nil && b.Account.Key == "codex:"+codexFakeAccount+":"+codexFakeUser {
				found = true
			}
		}
	}
	eq(t, found, true)
}

// A PC without Codex: one Claude helper, no Codex section, and the sync
// has a Codex block that says so (account null, not_signed_in; design 6.8).
func TestNoCodexKeepsTheClaudeOnlyTray(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	cancel, code := te.mainAsync("run")
	h := te.helpers.ready()
	fs.waitFor(t, 5*time.Second, func(r []protocol.Request) bool { return len(r) >= 1 })
	time.Sleep(100 * time.Millisecond)
	eq(t, itemText(h.lastView(), "h.codex"), "")
	eq(t, te.helpers.count(), 1)
	cancel()
	eq(t, waitCode(t, code), 0)
	v2 := fs.v2requests()
	if len(v2) == 0 {
		t.Fatal("no schema 2 sync")
	}
	var codexBlock *protocol.ProviderBlock
	for i, b := range v2[0].Providers {
		if b.Provider == "codex" {
			codexBlock = &v2[0].Providers[i]
		}
	}
	if codexBlock == nil || codexBlock.Account != nil || codexBlock.Auth.State != "not_signed_in" || len(codexBlock.Results) != 0 {
		t.Fatalf("codex block %+v", codexBlock)
	}
}

// waitStatus waits until status.json meets cond.
func waitStatus(t *testing.T, te *testEnv, cond func(status.Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s, err := status.Read(te.Dirs.File(paths.StatusFile)); err == nil && cond(s) {
			return
		}
		if time.Now().After(deadline) {
			s, err := status.Read(te.Dirs.File(paths.StatusFile))
			t.Fatalf("status not reached: %+v %v", s.Providers, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
