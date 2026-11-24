package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

var t0 = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

// Invented values only (public-repository rule): fake tokens, example.com.
const (
	fakeAccess    = "fake-codex-access-AAAAAAAAAAAAAAAA"
	fakeRefresh   = "fake-codex-refresh-BBBBBBBBBBBBBBBB"
	fakeAccountID = "00000000-0000-4000-8000-000000000001"
	fakeUserID    = "user-FakeUser0123456789"
	fakeEmail     = "user-c@example.com"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// jwt is an unsigned token with these claims; the agent reads claims only.
func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	must(t, err)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".fake-signature"
}

// authJSON is an auth.json of a ChatGPT sign-in whose access token expires
// at exp (zero: no exp claim).
func authJSON(t *testing.T, exp time.Time) string {
	t.Helper()
	access := map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}, "fake": fakeAccess}
	if !exp.IsZero() {
		access["exp"] = exp.Unix()
	}
	id := map[string]any{"email": fakeEmail, "exp": t0.Add(-200 * time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": fakeAccountID, "chatgpt_user_id": fakeUserID,
			"user_id": "user-Other", "chatgpt_plan_type": "plus"}}
	f := map[string]any{"OPENAI_API_KEY": nil, "auth_mode": "chatgpt", "last_refresh": t0.Add(-24 * time.Hour).Format(time.RFC3339Nano),
		"tokens": map[string]any{"access_token": jwt(t, access), "account_id": fakeAccountID, "id_token": jwt(t, id),
			"refresh_token": fakeRefresh}}
	b, err := json.Marshal(f)
	must(t, err)
	return string(b)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(body), 0o600))
}

func newReader(home string, clk clock.Clock) *Reader {
	r := NewReader(home, clk)
	r.sleep = func(time.Duration) {} // the retries' 50 ms
	return r
}

// One case per sign-in state (design D2).
func TestAuthStates(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, auth, cfg, state string
	}{
		{"no file", "", "", "not_signed_in"},
		{"no file, file store", "", "cli_auth_credentials_store = \"file\"\n", "not_signed_in"},
		{"keychain", "", "cli_auth_credentials_store = \"keyring\"\n", "unsupported_store"},
		{"auto store", "", "cli_auth_credentials_store = 'auto' # comment\n", "unsupported_store"},
		{"api key mode", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-fake-0123456789abcdef"}`, "", "api_key"},
		{"only an api key", `{"OPENAI_API_KEY":"sk-fake-0123456789abcdef","tokens":null}`, "", "api_key"},
		{"no tokens", `{"auth_mode":"chatgpt"}`, "", "not_signed_in"},
		{"expired", authJSON(t, t0.Add(-time.Minute)), "", "expired"},
		{"within the refresh window", authJSON(t, t0.Add(4*time.Minute)), "", "expired"},
		{"20 minutes left", authJSON(t, t0.Add(20*time.Minute)), "", "ok"},
		{"ok", authJSON(t, t0.Add(9*24*time.Hour)), "", "ok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if c.auth != "" {
				writeFile(t, filepath.Join(home, "auth.json"), c.auth)
			}
			if c.cfg != "" {
				writeFile(t, filepath.Join(home, "config.toml"), c.cfg)
			}
			l := newReader(home, clock.NewFake(t0)).Login(ctx)
			eq(t, l.Provider, provider.Codex)
			eq(t, l.State, c.state)
			if c.state == "ok" || c.state == "expired" {
				eq(t, l.AccountKey, "codex:"+fakeAccountID+":"+fakeUserID)
				eq(t, l.Email, fakeEmail)
				eq(t, l.Plan, "plus")
			} else if l.AccountKey != "" {
				t.Fatalf("account %q", l.AccountKey)
			}
		})
	}
}

// The access token's expiry is the sign-in's; without one, last_refresh
// plus 8 days (design B4, CodexBar's and Codex's own rule).
func TestFreshness(t *testing.T) {
	exp := t0.Add(time.Hour)
	eq(t, Freshness(Auth{ExpiresAt: exp}, t0), false)
	eq(t, Freshness(Auth{ExpiresAt: exp}, exp.Add(-5*time.Minute-time.Second)), false)
	eq(t, Freshness(Auth{ExpiresAt: exp}, exp.Add(-5*time.Minute)), true)
	eq(t, Freshness(Auth{LastRefresh: t0}, t0.Add(8*24*time.Hour)), false)
	eq(t, Freshness(Auth{LastRefresh: t0}, t0.Add(8*24*time.Hour+time.Second)), true)
	eq(t, Freshness(Auth{}, t0), true)
	// the access token's exp wins over an old last_refresh
	eq(t, Freshness(Auth{ExpiresAt: exp, LastRefresh: t0.Add(-30 * 24 * time.Hour)}, t0), false)
	r := newReader(t.TempDir(), clock.NewFake(t0))
	home := r.home
	writeFile(t, filepath.Join(home, "auth.json"), authJSON(t, time.Time{})) // no exp: last_refresh a day ago
	eq(t, r.Login(context.Background()).State, "ok")
}

// RF1: Codex rewrites auth.json while the agent reads it. A truncated
// file is read again 50 ms later (twice at most) and never shows as signed
// out.
func TestAuthReadRetriesPartialFile(t *testing.T) {
	home := t.TempDir()
	full := authJSON(t, t0.Add(9*24*time.Hour))
	path := filepath.Join(home, "auth.json")
	writeFile(t, path, full[:len(full)/2])
	r := NewReader(home, clock.NewFake(t0))
	slept := 0
	r.sleep = func(d time.Duration) {
		eq(t, d, 50*time.Millisecond)
		slept++
		writeFile(t, path, full) // Codex finished writing
	}
	l := r.Login(context.Background())
	eq(t, l.State, "ok")
	eq(t, slept, 1)

	// a file that stays broken: two retries, then the last good sign-in
	// is kept for a while (no flapping to signed out)
	writeFile(t, path, `{"tokens":`)
	slept = 0
	r.sleep = func(time.Duration) { slept++ }
	eq(t, r.Login(context.Background()).State, "ok")
	eq(t, slept, 2)
	for range 3 {
		r.Login(context.Background())
	}
	l = r.Login(context.Background())
	eq(t, l.State, "not_signed_in")
	if !strings.Contains(l.Problem, "could not be read") {
		t.Fatalf("problem %q", l.Problem)
	}
	// a missing file is read again too (Codex replaces it)
	must(t, os.Remove(path))
	slept = 0
	r.sleep = func(time.Duration) {
		slept++
		if slept == 2 {
			writeFile(t, path, full)
		}
	}
	eq(t, r.Login(context.Background()).State, "ok")
	eq(t, slept, 2)
}

// The agent never writes or locks auth.json: a read-only file in a
// read-only folder works, and many reads leave its content and times as
// they were.
func TestAuthFileNeverWritten(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.json")
	body := authJSON(t, t0.Add(9*24*time.Hour))
	writeFile(t, path, body)
	old := t0.Add(-48 * time.Hour)
	must(t, os.Chtimes(path, old, old))
	must(t, os.Chmod(path, 0o400))
	if runtime.GOOS != "windows" {
		must(t, os.Chmod(home, 0o500))
		t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	}
	clk := clock.NewFake(t0)
	r := newReader(home, clk)
	ctx := context.Background()
	for range 50 {
		eq(t, r.Login(ctx).State, "ok")
		must(t, r.WithToken(ctx, func(string, string) error { return nil }))
		clk.Advance(time.Minute)
	}
	st, err := os.Stat(path)
	must(t, err)
	if !st.ModTime().Equal(old) {
		t.Fatalf("mtime %v", st.ModTime())
	}
	b, err := os.ReadFile(path)
	must(t, err)
	eq(t, string(b), body)
	entries, err := os.ReadDir(home)
	must(t, err)
	if len(entries) != 1 {
		t.Fatalf("files %v (a lock or temp file?)", entries)
	}
}

// The sign-in is read only when the read rule says so (base spec 4.3): at
// start, when the file changed, once when the known expiry passed, and
// for every fetch.
func TestAuthReadTimingRule(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.json")
	exp := t0.Add(time.Hour)
	writeFile(t, path, authJSON(t, exp))
	clk := clock.NewFake(t0)
	r := newReader(home, clk)
	ctx := context.Background()
	r.Login(ctx)
	r.Login(ctx)
	eq(t, r.Reads(), 1)
	must(t, r.WithToken(ctx, func(string, string) error { return nil }))
	eq(t, r.Reads(), 2)
	clk.Set(exp) // passed: read once more
	eq(t, r.Login(ctx).State, "expired")
	r.Login(ctx)
	eq(t, r.Reads(), 3)
	writeFile(t, path, authJSON(t, t0.Add(48*time.Hour))) // Codex renewed it
	later := t0.Add(2 * time.Hour)
	must(t, os.Chtimes(path, later, later))
	eq(t, r.Login(ctx).State, "ok")
	eq(t, r.Reads(), 4)
}

// The token goes to the callback only, with the account id; an expired or
// missing sign-in calls nothing.
func TestWithToken(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.json")
	r := newReader(home, clock.NewFake(t0))
	ctx := context.Background()
	err := r.WithToken(ctx, func(string, string) error { t.Fatal("called"); return nil })
	eq(t, errors.Is(err, ErrNotSignedIn), true)
	writeFile(t, path, authJSON(t, t0.Add(-time.Minute)))
	retried := 0
	r.sleep = func(time.Duration) { retried++ }
	err = r.WithToken(ctx, func(string, string) error { t.Fatal("called"); return nil })
	eq(t, errors.Is(err, ErrExpired), true)
	eq(t, retried, 2) // read again in case Codex is just renewing it
	writeFile(t, path, authJSON(t, t0.Add(9*24*time.Hour)))
	var tok, acct string
	must(t, r.WithToken(ctx, func(a, b string) error { tok, acct = a, b; return nil }))
	if !strings.HasPrefix(tok, "eyJ") || acct != fakeAccountID {
		t.Fatalf("token %d bytes, account %q", len(tok), acct)
	}
	writeFile(t, path, `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-fake-0123456789abcdef"}`)
	err = r.WithToken(ctx, func(string, string) error { t.Fatal("called with an API key"); return nil })
	eq(t, errors.Is(err, ErrNotSignedIn), true)
}

// The account key names the account and the user (design D10); a value
// with other characters gives none (it would be refused by the server).
func TestAccountKeyValidation(t *testing.T) {
	eq(t, AccountKey(Auth{AccountID: fakeAccountID, UserID: fakeUserID}), "codex:"+fakeAccountID+":"+fakeUserID)
	eq(t, AccountKey(Auth{AccountID: "abc-123", UserID: "user_x-Y"}), "codex:abc-123:user_x-Y")
	for _, a := range []Auth{
		{AccountID: "", UserID: fakeUserID},
		{AccountID: fakeAccountID, UserID: ""},
		{AccountID: "a:b", UserID: fakeUserID},
		{AccountID: fakeAccountID, UserID: "user x"},
		{AccountID: "a_b", UserID: fakeUserID},
		{AccountID: fakeAccountID, UserID: "user/../x"},
		{AccountID: strings.Repeat("a", 100), UserID: strings.Repeat("b", 30)},
	} {
		if k := AccountKey(a); k != "" {
			t.Fatalf("%+v gave %q", a, k)
		}
	}
	// the user id comes from chatgpt_user_id, else user_id; claims of the
	// access token fill in what the id token lacks
	home := t.TempDir()
	access := jwt(t, map[string]any{"exp": t0.Add(48 * time.Hour).Unix(),
		"https://api.openai.com/auth":    map[string]any{"user_id": "user-FromAccess", "chatgpt_plan_type": "pro"},
		"https://api.openai.com/profile": map[string]any{"email": "user-d@example.com"}})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": access, "account_id": fakeAccountID}})
	writeFile(t, filepath.Join(home, "auth.json"), string(b))
	l := newReader(home, clock.NewFake(t0)).Login(context.Background())
	eq(t, l.AccountKey, "codex:"+fakeAccountID+":user-FromAccess")
	eq(t, l.Email, "user-d@example.com")
	eq(t, l.Plan, "pro")
	eq(t, l.State, "ok")
}

// No token string reaches the Login or its JSON, whatever the file holds.
func TestTokenNotInLogin(t *testing.T) {
	home := t.TempDir()
	body := authJSON(t, t0.Add(9*24*time.Hour))
	writeFile(t, filepath.Join(home, "auth.json"), body)
	var f struct {
		Tokens map[string]string `json:"tokens"`
	}
	must(t, json.Unmarshal([]byte(body), &f))
	l := newReader(home, clock.NewFake(t0)).Login(context.Background())
	b, err := json.Marshal(l)
	must(t, err)
	dump := string(b) + " " + strings.Join([]string{l.Problem, l.Email, l.AccountKey, l.Plan}, " ")
	for _, secret := range []string{fakeAccess, fakeRefresh, f.Tokens["access_token"], f.Tokens["id_token"], f.Tokens["refresh_token"]} {
		if strings.Contains(dump, secret) {
			t.Fatalf("a token is in the login: %s", dump)
		}
	}
	// a broken file's content is not quoted in the problem either
	writeFile(t, filepath.Join(home, "auth.json"), `{"tokens":{"access_token":"`+fakeAccess)
	r := newReader(home, clock.NewFake(t0))
	for range 4 {
		l = r.Login(context.Background())
	}
	if strings.Contains(l.Problem, "fake-codex") {
		t.Fatalf("problem %q", l.Problem)
	}
}

func TestHome(t *testing.T) {
	eq(t, Home(config.Config{}, "/h/u"), filepath.Join("/h/u", ".codex"))
	eq(t, Home(config.Config{CodexHome: "/w/codex"}, "/h/u"), "/w/codex")
}

// Neither the sign-in the agent keeps nor the reader has a token field.
func TestAuthHasNoTokenField(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Auth{}), reflect.TypeOf(Reader{}), reflect.TypeOf(provider.Login{})} {
		for i := 0; i < typ.NumField(); i++ {
			if n := strings.ToLower(typ.Field(i).Name); strings.Contains(n, "token") || strings.Contains(n, "secret") {
				t.Errorf("%s has field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}
