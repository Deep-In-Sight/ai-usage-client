package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

// builtFakeCodex is tools/fakecodex, built once for this package's tests
// (removed by TestMain).
var builtFakeCodex struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// fakeCodexCLI is a copy of fakecodex in its own folder that appends a
// line to marker whenever it is run, with any arguments.
func fakeCodexCLI(t *testing.T, marker string) string {
	t.Helper()
	b := &builtFakeCodex
	b.once.Do(func() {
		if b.dir, b.err = os.MkdirTemp("", "cli-fakecodex"); b.err != nil {
			return
		}
		goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, err := os.Stat(goBin); err != nil {
			goBin = "go"
		}
		b.path = filepath.Join(b.dir, "codex")
		if runtime.GOOS == "windows" {
			b.path += ".exe"
		}
		cmd := exec.Command(goBin, "build", "-o", b.path, "../../tools/fakecodex")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("building fakecodex: %v: %s", err, out)
		}
	})
	must(t, b.err)
	bin, err := os.ReadFile(b.path)
	must(t, err)
	p := filepath.Join(t.TempDir(), filepath.Base(b.path))
	must(t, os.WriteFile(p, bin, 0o755))
	must(t, os.WriteFile(p+".args", []byte("-record\n"+marker+"\n"), 0o600))
	return p
}

// codexPollServer is a schema 2 server that asks this PC to fetch Codex
// at every sync and keeps the request bodies.
type codexPollServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newCodexPollServer(t *testing.T) *codexPollServer {
	s := &codexPollServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"providers":[{"provider":"claude","role":"standby","fetch_now":false,"latest":null,"peers":{"devices":1,"pcs":1,"poller_hostname":null},"problem":null},`+
			`{"provider":"codex","role":"poller","fetch_now":true,"latest":null,"peers":{"devices":1,"pcs":1,"poller_hostname":null},"problem":null}],`+
			`"next_sync_seconds":15,"server_urls":[],"client_latest_version":null}`)
	}))
	t.Cleanup(s.Close)
	return s
}

// codexResults are the Codex results the server was sent.
func (s *codexPollServer) codexResults(t *testing.T) []protocol.ResultV2 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.ResultV2
	for _, b := range s.bodies {
		var req protocol.RequestV2
		must(t, json.Unmarshal([]byte(b), &req))
		for _, p := range req.Providers {
			if p.Provider == "codex" {
				out = append(out, p.Results...)
			}
		}
	}
	return out
}

// answeringChatGPT stands in for chatgpt.com with one fixed answer and
// counts the requests.
type answeringChatGPT struct {
	*fakeChatGPT
	mu sync.Mutex
	n  int
}

func newAnsweringChatGPT(t *testing.T, code int, body string) *answeringChatGPT {
	a := &answeringChatGPT{fakeChatGPT: &fakeChatGPT{}}
	a.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.n++
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *answeringChatGPT) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// codexAgentRun installs this build (version as given) on a PC where a
// fakecodex that records every run is first on PATH, Codex is signed in
// and chatgpt.com answers code/body, starts it and lets it fetch Codex
// rounds times (the fake clock stepped from timer to timer). It returns
// the server, the fake chatgpt.com, the test environment and the file
// fakecodex writes when it is run.
func codexAgentRun(t *testing.T, buildAs string, code int, body string, rounds int) (*codexPollServer, *answeringChatGPT, *testEnv, string) {
	t.Helper()
	old := buildVersion
	buildVersion = func() string { return buildAs }
	t.Cleanup(func() { buildVersion = old })
	t.Cleanup(func() { codex.AllowCLIRuns(false) })

	srv := newCodexPollServer(t)
	chat := newAnsweringChatGPT(t, code, body)
	te, _, _, _, _ := osEnv(t, runtime.GOOS)
	clk := te.Clock.(*clock.Fake)
	marker := filepath.Join(t.TempDir(), "codex-was-run")
	cli := fakeCodexCLI(t, marker)
	te.vars["PATH"] = filepath.Dir(cli) + string(os.PathListSeparator) + filepath.Join(t.TempDir(), "empty")
	te.vars["PATHEXT"] = ".COM;.EXE;.BAT;.CMD"
	// the CI build's switch, set here too: any other build must ignore it
	te.vars[testCodexCLIEnv] = cli
	te.CodexVersion = nil // the real `codex --version`, if anything asked for it
	te.CodexTransport = chat.transport()
	te.Syncer = func(cfg config.Config, ua string) engine.Syncer {
		return onlyTo{base: srv.URL, next: protocol.NewClient(cfg.IngestKey, ua, nil)}
	}
	codexSignIn(t, te.home)

	eq(t, te.main("install", "--config", writeCfg(t, `{"server_urls":["`+srv.URL+`"],"ingest_key":"test-ingest-key-0123456789"}`)), 0)
	c, err := config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	want, err := filepath.EvalSymlinks(cli)
	must(t, err)
	eq(t, c.CodexPath, want) // the fakecodex is also the recorded codex_path

	cancel, exit := te.mainAsync("run", "--headless")
	stop := make(chan struct{})
	stepped := make(chan struct{})
	go func() { // the fake time from one timer to the next
		defer close(stepped)
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
			if next, ok := clk.Next(); ok {
				clk.Set(next)
			}
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for len(srv.codexResults(t)) < rounds {
		if time.Now().After(deadline) {
			close(stop)
			t.Fatalf("%d Codex results reached the server, want %d (chatgpt.com asked %d times)", len(srv.codexResults(t)), rounds, chat.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-stepped
	cancel()
	eq(t, waitCode(t, exit), 0)
	return srv, chat, te, marker
}

// Ruling F1 (review I-1): a release build never starts the Codex CLI in
// any form. A fakecodex that records every run is first on PATH and is
// the codex_path install recorded, the CI build's fallback switch names
// it too, and chatgpt.com turns the direct request away: after install,
// start and several fetch rounds, codex was never run, and what the
// server got for Codex is the direct request's own answer, every round.
func TestReleaseBuildNeverRunsCodex(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		body  string
		usage string // what the result carries as usage
	}{
		{"403", http.StatusForbidden, `{"detail":"Unusual activity has been detected from your device. Try again later."}`, "null"},
		{"200 without limits", http.StatusOK, `{"plan_type":"plus","rate_limit":null,"additional_rate_limits":null}`,
			`{"plan_type":"plus","rate_limit":null,"additional_rate_limits":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rounds = 4
			srv, chat, te, marker := codexAgentRun(t, "0.8.3", tc.code, tc.body, rounds)
			if b, err := os.ReadFile(marker); !os.IsNotExist(err) {
				t.Fatalf("codex was run: %q %v", b, err)
			}
			eq(t, codex.CLIRunsAllowed(), false)
			if chat.count() < rounds {
				t.Fatalf("chatgpt.com asked %d times, want >= %d", chat.count(), rounds)
			}
			res := srv.codexResults(t)
			for i, r := range res {
				src := string(r.Source)
				if r.HTTPStatus != tc.code || src != "wham" || strings.TrimSpace(string(r.Usage)) != tc.usage {
					t.Fatalf("result %d: HTTP %d source %q usage %s", i, r.HTTPStatus, src, r.Usage)
				}
			}
			s, err := status.Read(te.Dirs.File(paths.StatusFile))
			must(t, err)
			ps := s.Providers[provider.Codex]
			eq(t, ps.LastFetchSource, "wham")
			eq(t, ps.FallbackCLI+ps.FallbackProblem, "")
		})
	}
}

// The control: a CI build (version 0.0.0-ci) with its test switch does
// run the named fakecodex as the fallback, and the same check sees it.
func TestCIBuildSwitchRunsTheFakeCodex(t *testing.T) {
	srv, _, _, marker := codexAgentRun(t, "0.0.0-ci", http.StatusForbidden, `{"detail":"no"}`, 2)
	b, err := os.ReadFile(marker)
	must(t, err)
	if !strings.Contains(string(b), "ARGS app-server --listen stdio://") {
		t.Fatalf("fakecodex record: %q", b)
	}
	found := false
	for _, r := range srv.codexResults(t) {
		if r.Source == "app-server" && r.HTTPStatus == 200 {
			found = true
		}
	}
	eq(t, found, true)
}
