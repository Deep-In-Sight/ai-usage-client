// Package codex is the Codex provider's own code: reading the Codex CLI's
// sign-in (auth.json, read-only), its config.toml, finding the codex
// executable and (fetch.go) asking chatgpt.com for the plan usage. The
// access token stays inside this package: it is read from auth.json for
// each request and handed to the request inside one callback (WithToken),
// never kept, written, logged or refreshed. Refreshing is the Codex CLI's
// job (design D2).
package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// Auth is what the agent uses of auth.json, without any token.
type Auth struct {
	Mode        string    // auth_mode: "chatgpt" | "apikey" | ""
	AccountID   string    // tokens.account_id (the ChatGPT-Account-Id header)
	UserID      string    // the id token's chatgpt_user_id, else user_id
	Email       string    // the id token's email
	Plan        string    // the id token's chatgpt_plan_type
	ExpiresAt   time.Time // the access token's exp; zero if unreadable
	LastRefresh time.Time // last_refresh; zero if unreadable
	apiKey      bool      // only an API key (no ChatGPT tokens)
}

// Home is the Codex folder: codex_home from config.json (recorded at
// install from CODEX_HOME), else <home>/.codex. The autostart
// environment's CODEX_HOME is not looked at (design 6.3).
func Home(cfg config.Config, userHome string) string {
	if cfg.CodexHome != "" {
		return cfg.CodexHome
	}
	return filepath.Join(userHome, ".codex")
}

var (
	accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	userIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// maxAccountKey is the server's limit for a schema 2 account key.
const maxAccountKey = 128

// AccountKey is the account as the server keys it (design D10):
// "codex:<account_id>:<user_id>", "" when either part is missing or has
// other characters.
func AccountKey(a Auth) string {
	if !accountIDPattern.MatchString(a.AccountID) || !userIDPattern.MatchString(a.UserID) {
		return ""
	}
	k := "codex:" + a.AccountID + ":" + a.UserID
	if len(k) > maxAccountKey {
		return ""
	}
	return k
}

const (
	// refreshWindow and refreshInterval are when a sign-in counts as
	// expired (design B4).
	refreshWindow   = 5 * time.Minute
	refreshInterval = 8 * 24 * time.Hour
)

// Freshness reports whether the sign-in needs Codex to renew it: from 5
// minutes before the access token's exp when that is known, else 8 days
// after last_refresh (or always, when neither is known).
//
// Ported from steipete/CodexBar (MIT) docs/codex.md "Codex auth.json needs
// refresh" and Sources/CodexBarCore/Providers/Codex/CodexOAuth/CodexOAuthCredentials.swift
// needsRefresh @ a26ca57: the access token's exp with a 5-minute window,
// else last_refresh plus 8 days (Codex's own rule).
func Freshness(a Auth, now time.Time) (expired bool) {
	if !a.ExpiresAt.IsZero() {
		return !now.Before(a.ExpiresAt.Add(-refreshWindow))
	}
	if !a.LastRefresh.IsZero() {
		return now.Sub(a.LastRefresh) > refreshInterval
	}
	return true
}

// Errors of WithToken; they never carry file content.
var (
	ErrNotSignedIn = errors.New("codex: no usable Codex sign-in")
	ErrExpired     = errors.New("codex: the Codex sign-in needs renewing (run Codex once)")
)

const (
	maxAuthFile      = 256 << 10
	readRetries      = 2
	retryGap         = 50 * time.Millisecond
	badReadsToGiveUp = 3
)

// authFile is the part of auth.json the agent reads. The refresh token is
// not part of it: it is never even decoded.
type authFile struct {
	AuthMode    string  `json:"auth_mode"`
	APIKey      *string `json:"OPENAI_API_KEY"`
	LastRefresh string  `json:"last_refresh"`
	Tokens      *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
		IDToken     string `json:"id_token"`
	} `json:"tokens"`
}

// claims are the JWT claims the agent reads (signatures are not checked:
// the claims only name the account; the service checks the token).
type claims struct {
	Exp     *float64 `json:"exp"`
	Email   string   `json:"email"`
	Auth    *claimsAuth
	Profile *struct {
		Email string `json:"email"`
	}
}

type claimsAuth struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	ChatGPTUserID    string `json:"chatgpt_user_id"`
	UserID           string `json:"user_id"`
	PlanType         string `json:"chatgpt_plan_type"`
}

func decodeClaims(token string) claims {
	var c claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return c
	}
	_ = json.Unmarshal(raw["exp"], &c.Exp)
	_ = json.Unmarshal(raw["email"], &c.Email)
	if v, ok := raw["https://api.openai.com/auth"]; ok {
		_ = json.Unmarshal(v, &c.Auth)
	}
	if v, ok := raw["https://api.openai.com/profile"]; ok {
		_ = json.Unmarshal(v, &c.Profile)
	}
	return c
}

var errNotJSON = errors.New("the Codex sign-in file is not valid JSON")

// parseAuth reads auth.json's content; the access token is returned to
// the caller only.
func parseAuth(b []byte) (Auth, string, error) {
	var f authFile
	if !json.Valid(bytes.TrimSpace(b)) || json.Unmarshal(b, &f) != nil {
		return Auth{}, "", errNotJSON
	}
	a := Auth{Mode: strings.ToLower(f.AuthMode)}
	if t, err := time.Parse(time.RFC3339Nano, f.LastRefresh); err == nil {
		a.LastRefresh = t
	}
	hasKey := f.APIKey != nil && strings.TrimSpace(*f.APIKey) != ""
	if f.Tokens == nil || f.Tokens.AccessToken == "" {
		a.apiKey = hasKey
		return a, "", nil
	}
	access, id := decodeClaims(f.Tokens.AccessToken), decodeClaims(f.Tokens.IDToken)
	if access.Exp != nil && *access.Exp > 0 {
		a.ExpiresAt = time.Unix(int64(*access.Exp), 0).UTC()
	}
	a.AccountID = f.Tokens.AccountID
	first := func(vs ...string) string {
		for _, v := range vs {
			if v != "" {
				return v
			}
		}
		return ""
	}
	var ia, aa claimsAuth
	if id.Auth != nil {
		ia = *id.Auth
	}
	if access.Auth != nil {
		aa = *access.Auth
	}
	if a.AccountID == "" {
		a.AccountID = first(ia.ChatGPTAccountID, aa.ChatGPTAccountID)
	}
	a.UserID = first(ia.ChatGPTUserID, ia.UserID, aa.ChatGPTUserID, aa.UserID)
	a.Plan = first(ia.PlanType, aa.PlanType)
	a.Email = id.Email
	if a.Email == "" && access.Profile != nil {
		a.Email = access.Profile.Email
	}
	if a.Email == "" {
		a.Email = access.Email
	}
	return a, f.Tokens.AccessToken, nil
}

type stamp struct {
	exists bool
	mod    time.Time
	size   int64
}

func stampOf(path string) stamp {
	st, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{exists: true, mod: st.ModTime(), size: st.Size()}
}

// readCapped reads at most limit bytes of a file opened read-only. On
// Windows os.Open shares the file for reading and writing, so Codex can
// write it meanwhile; it is closed at once.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// Reader reads the Codex sign-in under the base spec's read rule (4.3): at
// start, when auth.json or config.toml changes, once when the known expiry
// passes, and for every fetch. It keeps no token.
type Reader struct {
	home  string
	clk   clock.Clock
	sleep func(time.Duration)

	mu         sync.Mutex
	reads      int
	have       bool
	auth       Auth
	state      string // the state read: "" (signed in: by Freshness at the time asked), not_signed_in, unsupported_store, api_key
	problem    string
	good       Auth
	haveGood   bool
	bad        int
	retry      bool
	authStamp  stamp
	cfgStamp   stamp
	expiryDone time.Time
	// rejected: the usage service refused the token (401); the sign-in
	// counts as expired until auth.json changes (design D2)
	rejected      bool
	rejectedStamp stamp
}

// Rejected notes that the usage service refused the token (HTTP 401):
// until auth.json changes (Codex renewed the sign-in) the sign-in counts
// as expired and WithToken calls nothing.
func (r *Reader) Rejected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejected, r.rejectedStamp = true, stampOf(r.authPath())
}

// stillRejected reports whether a 401 still stands (s.mu held).
func (r *Reader) stillRejected() bool {
	if r.rejected && stampOf(r.authPath()) != r.rejectedStamp {
		r.rejected = false
	}
	return r.rejected
}

// NewReader reads the sign-in in the Codex folder home. It reads nothing
// yet.
func NewReader(home string, clk clock.Clock) *Reader {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Reader{home: home, clk: clk, sleep: time.Sleep}
}

func (r *Reader) authPath() string   { return filepath.Join(r.home, "auth.json") }
func (r *Reader) configPath() string { return filepath.Join(r.home, "config.toml") }

// Home is the Codex folder read.
func (r *Reader) Home() string { return r.home }

// Reads is how many times the sign-in was read (tests).
func (r *Reader) Reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// Login is the current sign-in as the engine sees it (design D2), reading
// the files again only when the rule says so.
func (r *Reader) Login(ctx context.Context) provider.Login {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clk.Now()
	if r.needRead(now) {
		r.read(ctx)
	}
	return r.login(now)
}

func (r *Reader) needRead(now time.Time) bool {
	if !r.have || r.retry {
		return true
	}
	if stampOf(r.authPath()) != r.authStamp || stampOf(r.configPath()) != r.cfgStamp {
		return true
	}
	if exp := r.auth.ExpiresAt; r.state == "" && !exp.IsZero() && Freshness(r.auth, now) && !r.expiryDone.Equal(exp) {
		return true // read once more when it turns expired: Codex may have renewed it
	}
	return false
}

func (r *Reader) login(now time.Time) provider.Login {
	l := provider.Login{Provider: provider.Codex, State: r.state, Problem: r.problem}
	if r.state != "" {
		return l
	}
	a := r.auth
	l.AccountKey, l.Email, l.Plan, l.ExpiresAt = AccountKey(a), a.Email, a.Plan, a.ExpiresAt
	l.State = "ok"
	switch {
	case Freshness(a, now):
		l.State = "expired"
	case r.stillRejected():
		l.State, l.Problem = "expired", "the usage service refused the Codex sign-in (HTTP 401)"
	}
	if l.AccountKey == "" && l.Problem == "" {
		l.Problem = "the Codex sign-in names no account"
	}
	return l
}

// readAuth reads auth.json, again after 50 ms (twice at most) when it is
// missing, unreadable or not JSON: Codex may be replacing it.
//
// Ported from steipete/CodexBar (MIT) docs/codex.md "OAuth API" and
// Sources/CodexBarCore/Providers/Codex/CodexProviderDescriptor.swift
// loadCredentials @ a26ca57: a missing, unreadable or partly written
// auth.json is read again twice, 50 ms apart; the file is never written.
func (r *Reader) readAuth() (Auth, string, error) {
	var lastErr error
	for i := 0; ; i++ {
		b, err := readCapped(r.authPath(), maxAuthFile)
		if err == nil {
			a, tok, perr := parseAuth(b)
			if perr == nil {
				return a, tok, nil
			}
			err = perr
		}
		lastErr = err
		if i == readRetries {
			return Auth{}, "", lastErr
		}
		r.sleep(retryGap)
	}
}

// read reads both files and stores the result; the access token is
// returned to the caller only ("" when not signed in).
func (r *Reader) read(_ context.Context) string {
	r.reads++
	r.have = true
	r.cfgStamp = stampOf(r.configPath())
	a, tok, err := r.readAuth()
	r.authStamp = stampOf(r.authPath())
	r.problem = ""
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.bad, r.retry = 0, false
		r.auth, r.state = Auth{}, "not_signed_in"
		r.problem = "no Codex sign-in found"
		if store := strings.ToLower(ReadConfig(r.home).CredentialsStore); store == "keyring" || store == "auto" {
			r.state, r.problem = "unsupported_store", "the Codex sign-in is in the system keychain"
		}
		return ""
	case err != nil:
		r.bad++
		why := "the Codex sign-in file could not be read"
		if errors.Is(err, errNotJSON) {
			why = "the Codex sign-in file could not be read (not valid JSON)"
		}
		if r.haveGood && r.bad < badReadsToGiveUp {
			r.retry = true // keep the last good sign-in for now
			r.auth, r.state = r.good, ""
			return ""
		}
		r.retry = r.bad < badReadsToGiveUp
		r.auth, r.state, r.problem = Auth{}, "not_signed_in", why
		return ""
	}
	r.bad, r.retry = 0, false
	r.auth = a
	switch {
	case a.Mode == "apikey" || a.Mode == "api_key" || a.apiKey && tok == "":
		r.state, r.problem = "api_key", "Codex is signed in with an API key (no plan limits)"
		return ""
	case tok == "":
		r.state, r.problem = "not_signed_in", "the Codex sign-in has no ChatGPT tokens"
		return ""
	}
	r.state = ""
	r.good, r.haveGood = a, true
	if now := r.clk.Now(); !a.ExpiresAt.IsZero() && Freshness(a, now) {
		r.expiryDone = a.ExpiresAt
	}
	return tok
}

// Usable reads auth.json again and returns the sign-in (no token) when it
// is usable now: signed in with ChatGPT, not due for renewal and not
// refused by the usage service (a 401 stands until auth.json changes).
// The fallback asks it right before it would start codex.
func (r *Reader) Usable(ctx context.Context) (Auth, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok := r.read(ctx)
	l := r.login(r.clk.Now())
	if l.State != "ok" || tok == "" {
		return Auth{}, false
	}
	return r.auth, true
}

// Now is the reader's clock.
func (r *Reader) Now() time.Time { return r.clk.Now() }

// WithToken reads the sign-in and, when it is usable, calls fn with the
// access token and the account id. fn must not keep the token. A sign-in
// due for renewal is read again twice, 50 ms apart (Codex may be just
// writing the renewed one), and fn is then not called.
func (r *Reader) WithToken(ctx context.Context, fn func(accessToken, accountID string) error) error {
	r.mu.Lock()
	var tok string
	var l provider.Login
	for i := 0; ; i++ {
		tok = r.read(ctx)
		l = r.login(r.clk.Now())
		if l.State != "expired" || i == readRetries {
			break
		}
		r.sleep(retryGap)
	}
	account := r.auth.AccountID
	r.mu.Unlock()
	switch {
	case l.State == "expired":
		return ErrExpired
	case l.State != "ok" || tok == "":
		return ErrNotSignedIn
	}
	return fn(tok, account)
}
