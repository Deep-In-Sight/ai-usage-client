// fakeserver is a stand-in for the team's AI Usage server in CI: it
// takes syncs in schema 1 and 2 (-schema1-only: a server from before
// schema 2, which refuses it), always answers "standby" (so the agent
// never asks Anthropic),
// serves the release files and the CI install scripts, and counts what it
// saw. /stats shows counts and fake values only, never a key or a token.
// POST /weekly?percent=N changes the weekly percentage it answers with;
// POST /hold?seconds=N makes syncs wait that long before the answer (a
// server that takes a sync and does not answer in time; 0 ends it).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is the server's settings.
type Config struct {
	Key     string // the ingest key syncs must carry
	Token   string // the fake access token that must never arrive here
	Files   string // release files
	Scripts string // install-ci.sh and install-ci.ps1
	// Schema1Only answers schema 2 with 400 unsupported_schema.
	Schema1Only bool
}

type device struct {
	Syncs         int    `json:"syncs"`
	LastMode      string `json:"last_mode"`
	LastVersion   string `json:"last_version"`
	LastAuthState string `json:"last_auth_state"`
	LastEmail     string `json:"last_email"`
	WantFresh     int    `json:"want_fresh"`
	LastSchema    int    `json:"last_schema"`
	Machine       bool   `json:"machine"` // the last sync had a machine key (never shown)
	Uninstalls    int    `json:"uninstall_events"`
}

type server struct {
	cfg        Config
	mu         sync.Mutex
	syncs      int
	badKey     int
	tokenLeaks int
	devices    map[string]*device
	weekly     int // the weekly_all percentage syncs are answered with
	hold       time.Duration
}

func newServer(cfg Config) http.Handler {
	s := &server{cfg: cfg, devices: map[string]*device{}, weekly: 42}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/v1/sync", s.sync)
	mux.HandleFunc("GET /files/{name}", s.file)
	mux.HandleFunc("GET /stats", s.stats)
	mux.HandleFunc("POST /weekly", s.setWeekly)
	mux.HandleFunc("POST /hold", s.setHold)
	return s.leakCheck(mux)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// leakCheck counts requests whose headers or body hold the fake token.
func (s *server) leakCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		leak := s.cfg.Token != "" && bytes.Contains(body, []byte(s.cfg.Token))
		for _, vs := range r.Header {
			for _, v := range vs {
				leak = leak || s.cfg.Token != "" && strings.Contains(v, s.cfg.Token)
			}
		}
		leak = leak || s.cfg.Token != "" && strings.Contains(r.URL.String(), s.cfg.Token)
		if leak {
			s.mu.Lock()
			s.tokenLeaks++
			s.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

func base(r *http.Request) string { return "http://" + r.Host }

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *server) sync(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.cfg.Key {
		s.mu.Lock()
		s.badKey++
		s.mu.Unlock()
		writeJSON(w, 401, map[string]string{"error": "bad_ingest_key"})
		return
	}
	s.mu.Lock()
	hold := s.hold
	s.mu.Unlock()
	if hold > 0 {
		select {
		case <-time.After(hold):
		case <-r.Context().Done(): // the client gave up
			return
		}
	}
	var req struct {
		Schema int `json:"schema"`
		Client struct {
			Version string `json:"version"`
			Mode    string `json:"mode"`
		} `json:"client"`
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
		Auth struct {
			State string `json:"state"`
		} `json:"auth"`
		Account *struct {
			Email string `json:"email"`
		} `json:"account"`
		WantFresh bool            `json:"want_fresh"`
		Machine   json.RawMessage `json:"machine"`
		Event     *string         `json:"event"`
		Providers []struct {
			Provider string `json:"provider"`
			Account  *struct {
				Email string `json:"email"`
			} `json:"account"`
			Auth struct {
				State string `json:"state"`
			} `json:"auth"`
			WantFresh bool `json:"want_fresh"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad_request"})
		return
	}
	if req.Schema != 1 && (req.Schema != 2 || s.cfg.Schema1Only) {
		writeJSON(w, 400, map[string]string{"error": "unsupported_schema"})
		return
	}
	if req.Schema == 2 { // the Claude block in schema 1 terms
		for _, p := range req.Providers {
			if p.Provider == "claude" {
				req.Auth.State, req.Account, req.WantFresh = p.Auth.State, p.Account, p.WantFresh
			}
		}
	}
	s.mu.Lock()
	s.syncs++
	d := s.devices[req.Device.ID]
	if d == nil {
		d = &device{}
		s.devices[req.Device.ID] = d
	}
	d.Syncs++
	d.LastSchema = req.Schema
	d.Machine = len(req.Machine) > 0 && string(req.Machine) != "null"
	if req.Event != nil && *req.Event == "uninstall" {
		d.Uninstalls++
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"providers": []any{}, "next_sync_seconds": 60, "server_urls": []string{base(r)}, "client_latest_version": nil})
		return
	}
	d.LastMode, d.LastVersion, d.LastAuthState = req.Client.Mode, req.Client.Version, req.Auth.State
	if req.Account != nil {
		d.LastEmail = req.Account.Email
	}
	if req.WantFresh {
		d.WantFresh++
	}
	weekly := s.weekly
	s.mu.Unlock()

	now := time.Now()
	resets := stamp(now.Add(48 * time.Hour))
	latest := map[string]any{
		"fetched_at": stamp(now.Add(-60 * time.Second)),
		"usage": map[string]any{"limits": []any{
			map[string]any{"kind": "weekly_all", "group": "weekly", "percent": weekly, "severity": "normal", "resets_at": resets, "scope": nil},
			map[string]any{"kind": "weekly_scoped", "group": "weekly", "percent": 40, "severity": "normal", "resets_at": resets,
				"scope": map[string]any{"model": map[string]any{"id": nil, "display_name": "Fable"}, "surface": nil}},
		}},
	}
	if req.Schema == 2 {
		blocks := []any{}
		for _, p := range req.Providers {
			b := map[string]any{"provider": p.Provider, "role": "standby", "fetch_now": false, "latest": nil,
				"peers": map[string]any{"devices": 0, "pcs": 0, "poller_hostname": nil}, "problem": nil}
			if p.Provider == "claude" {
				l := map[string]any{"source": nil}
				for k, v := range latest {
					l[k] = v
				}
				b["latest"] = l
				b["peers"] = map[string]any{"devices": 2, "pcs": 2, "poller_hostname": "TEST-PC-01"}
			}
			blocks = append(blocks, b)
		}
		writeJSON(w, 200, map[string]any{"providers": blocks, "next_sync_seconds": 15, "server_urls": []string{base(r)}, "client_latest_version": nil})
		return
	}
	writeJSON(w, 200, map[string]any{
		"role": "standby", "fetch_now": false, "next_sync_seconds": 15,
		"latest":                latest,
		"peers":                 map[string]any{"devices": 2, "poller_hostname": "TEST-PC-01"},
		"server_urls":           []string{base(r)},
		"client_latest_version": nil,
	})
}

func (s *server) file(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name != path.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	if name == "install-ci.sh" || name == "install-ci.ps1" {
		b, err := os.ReadFile(filepath.Join(s.cfg.Scripts, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		b = bytes.ReplaceAll(b, []byte("@BASE@"), []byte(base(r)))
		b = bytes.ReplaceAll(b, []byte("@KEY@"), []byte(s.cfg.Key))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	p := filepath.Join(s.cfg.Files, name)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, p)
}

// setWeekly changes the weekly_all percentage of later sync answers
// (POST /weekly?percent=0..100), so a check can watch the tray follow.
func (s *server) setWeekly(w http.ResponseWriter, r *http.Request) {
	v, err := strconv.Atoi(r.URL.Query().Get("percent"))
	if err != nil || v < 0 || v > 100 {
		writeJSON(w, 400, map[string]string{"error": "percent must be 0..100"})
		return
	}
	s.mu.Lock()
	s.weekly = v
	s.mu.Unlock()
	w.WriteHeader(204)
}

// setHold makes later syncs wait before they are answered (POST
// /hold?seconds=0..600; 0: answer at once again).
func (s *server) setHold(w http.ResponseWriter, r *http.Request) {
	v, err := strconv.Atoi(r.URL.Query().Get("seconds"))
	if err != nil || v < 0 || v > 600 {
		writeJSON(w, 400, map[string]string{"error": "seconds must be 0..600"})
		return
	}
	s.mu.Lock()
	s.hold = time.Duration(v) * time.Second
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"syncs": s.syncs, "bad_key": s.badKey, "token_leaks": s.tokenLeaks, "devices": s.devices})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18700", "address to listen on")
	var cfg Config
	flag.StringVar(&cfg.Key, "key", "", "ingest key")
	flag.StringVar(&cfg.Token, "token", "", "fake access token to watch for")
	flag.StringVar(&cfg.Files, "files", ".", "release files")
	flag.StringVar(&cfg.Scripts, "scripts", ".", "install-ci scripts")
	flag.BoolVar(&cfg.Schema1Only, "schema1-only", false, "refuse schema 2 (400 unsupported_schema), as a server from before it")
	flag.Parse()
	if cfg.Key == "" {
		fmt.Fprintln(os.Stderr, "fakeserver: -key is required")
		os.Exit(2)
	}
	log.Printf("fakeserver listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, newServer(cfg)))
}
