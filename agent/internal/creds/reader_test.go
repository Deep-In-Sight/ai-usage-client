package creds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/clock"
)

var t0 = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

func TestCurrentReadsFileAndAccount(t *testing.T) {
	h := t.TempDir()
	expiry := t0.Add(8 * time.Hour).Truncate(time.Millisecond)
	writeCreds(t, filepath.Join(h, ".claude"), expiry)
	writeAccount(t, filepath.Join(h, ".claude.json"))
	r := NewReader(Options{Home: h, Clock: clock.NewFake(t0)})
	l := r.Current(context.Background())
	eq(t, l.State(t0), "ok")
	eq(t, l.ExpiresAt.Equal(expiry), true)
	eq(t, l.Account.UUID, "acc-1")
	eq(t, l.Account.Email, "user-a@example.com")
	eq(t, l.Account.DisplayName, "User A")
	eq(t, l.Account.OrganizationName, "Example")
	eq(t, l.Account.OrganizationType, "claude_max")
	eq(t, l.SubscriptionType, "max")
	eq(t, l.RateLimitTier, "default_claude_max_20x")
	eq(t, l.Source, "file")
}

func TestClaudeConfigDirMovesBothFiles(t *testing.T) {
	h := t.TempDir()
	dir := filepath.Join(t.TempDir(), "claude")
	// decoys in the default places
	writeCreds(t, filepath.Join(h, ".claude"), t0.Add(-time.Hour))
	must(t, os.WriteFile(filepath.Join(h, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"decoy"}}`), 0o600))
	writeCreds(t, dir, t0.Add(time.Hour))
	writeAccount(t, filepath.Join(dir, ".claude.json"))
	r := NewReader(Options{Home: h, ClaudeConfigDir: dir, Clock: clock.NewFake(t0)})
	l := r.Current(context.Background())
	eq(t, l.State(t0), "ok")
	eq(t, l.Account.UUID, "acc-1")
}

func TestNoFileIsNotSignedIn(t *testing.T) {
	r := NewReader(Options{Home: t.TempDir(), Clock: clock.NewFake(t0)})
	l := r.Current(context.Background())
	eq(t, l.State(t0), "not_signed_in")
	eq(t, l.Account == nil, true)
	err := r.WithToken(context.Background(), func(string) error { t.Fatal("called"); return nil })
	eq(t, errors.Is(err, ErrNotSignedIn), true)
}

func TestReadTimingRule(t *testing.T) {
	ctx := context.Background()
	h := t.TempDir()
	expiry := t0.Add(8 * time.Hour)
	credsPath := writeCreds(t, filepath.Join(h, ".claude"), expiry)
	writeAccount(t, filepath.Join(h, ".claude.json"))
	clk := clock.NewFake(t0)
	r := NewReader(Options{Home: h, Clock: clk})
	r.Current(ctx)
	for i := 0; i < 60; i++ {
		clk.Advance(time.Minute)
		r.Current(ctx)
	}
	eq(t, r.Reads(), 1) // an hour of ticks, file unchanged: one read
	touchLater(t, credsPath, time.Second)
	r.Current(ctx)
	eq(t, r.Reads(), 2) // mtime changed
	r.Current(ctx)
	eq(t, r.Reads(), 2)
	clk.Set(expiry.Add(time.Second))
	r.Current(ctx)
	r.Current(ctx)
	eq(t, r.Reads(), 3) // once per expiry value
	_ = r.WithToken(ctx, func(string) error { return nil })
	eq(t, r.Reads(), 4) // a fetch always reads
	r.Current(ctx)
	eq(t, r.Reads(), 4)
	// the file disappears: noticed through stat
	must(t, os.Remove(credsPath))
	eq(t, r.Current(ctx).State(clk.Now()), "not_signed_in")
	eq(t, r.Reads(), 5)
}

func TestWithTokenPassesToken(t *testing.T) {
	h := t.TempDir()
	writeCreds(t, filepath.Join(h, ".claude"), t0.Add(time.Hour))
	r := NewReader(Options{Home: h, Clock: clock.NewFake(t0)})
	var got string
	must(t, r.WithToken(context.Background(), func(tok string) error { got = tok; return nil }))
	eq(t, got, fakeToken)
	want := errors.New("inner")
	eq(t, r.WithToken(context.Background(), func(string) error { return want }), want)
}

func TestWithTokenExpiredDoesNotCall(t *testing.T) {
	h := t.TempDir()
	writeCreds(t, filepath.Join(h, ".claude"), t0.Add(-time.Minute))
	r := NewReader(Options{Home: h, Clock: clock.NewFake(t0)})
	called := false
	err := r.WithToken(context.Background(), func(string) error { called = true; return nil })
	eq(t, called, false)
	eq(t, errors.Is(err, ErrExpired), true)
	eq(t, r.Current(context.Background()).State(t0), "expired")
}

func TestCurrentKeepsLastGoodOnParseError(t *testing.T) {
	ctx := context.Background()
	h := t.TempDir()
	p := writeCreds(t, filepath.Join(h, ".claude"), t0.Add(time.Hour))
	r := NewReader(Options{Home: h, Clock: clock.NewFake(t0)})
	eq(t, r.Current(ctx).State(t0), "ok")
	must(t, os.WriteFile(p, []byte(`{"claudeAiOauth":{"acc`), 0o600))
	touchLater(t, p, time.Second)
	eq(t, r.Current(ctx).State(t0), "ok")
	eq(t, r.Current(ctx).State(t0), "ok")
	l := r.Current(ctx)
	eq(t, l.State(t0), "not_signed_in")
	eq(t, l.Problem, "credentials file is not valid JSON")
	eq(t, r.Reads(), 4)
	r.Current(ctx)
	eq(t, r.Reads(), 4) // no more retries until the file changes
	// the write finishes: back to ok
	writeCreds(t, filepath.Join(h, ".claude"), t0.Add(time.Hour))
	touchLater(t, p, 2*time.Second)
	eq(t, r.Current(ctx).State(t0), "ok")
}

func TestErrorsNeverContainToken(t *testing.T) {
	ctx := context.Background()
	h := t.TempDir()
	p := writeCreds(t, filepath.Join(h, ".claude"), t0.Add(-time.Minute))
	writeAccount(t, filepath.Join(h, ".claude.json"))
	r := NewReader(Options{Home: h, Clock: clock.NewFake(t0)})
	login := r.Current(ctx)
	err := r.WithToken(ctx, func(string) error { return nil })
	s := fmt.Sprintf("%v %+v %#v %v", login, login, err, r)
	must(t, os.WriteFile(p, []byte(`{"claudeAiOauth":{"accessToken":"`+fakeToken+`"`), 0o600))
	touchLater(t, p, time.Second)
	for i := 0; i < 3; i++ {
		l := r.Current(ctx)
		s += fmt.Sprintf("%v %+v %#v", l, l, r.WithToken(ctx, func(string) error { return nil }))
	}
	if strings.Contains(s, "fake-access-token") || strings.Contains(s, "fake-refresh") {
		t.Fatal(s)
	}
}

func TestKeychainFirstThenFile(t *testing.T) {
	ctx := context.Background()
	h := t.TempDir()
	writeCreds(t, filepath.Join(h, ".claude"), t0.Add(time.Hour))
	var asked []string
	ok := func(_ context.Context, service string) ([]byte, error) {
		asked = append(asked, service)
		return []byte(credsJSON(t0.Add(2 * time.Hour))), nil
	}
	r := NewReader(Options{Home: h, UseKeychain: true, Keychain: ok, KeychainService: "svc-a", Clock: clock.NewFake(t0)})
	l := r.Current(ctx)
	eq(t, l.Source, "keychain")
	eq(t, l.ExpiresAt.Equal(t0.Add(2*time.Hour)), true)
	eq(t, asked, []string{"svc-a"})
	fail := func(context.Context, string) ([]byte, error) { return nil, errors.New("keychain: denied") }
	r = NewReader(Options{Home: h, UseKeychain: true, Keychain: fail, Clock: clock.NewFake(t0)})
	l = r.Current(ctx)
	eq(t, l.Source, "file")
	eq(t, l.State(t0), "ok")
	// without UseKeychain the keychain is never asked
	asked = nil
	r = NewReader(Options{Home: h, Keychain: ok, Clock: clock.NewFake(t0)})
	eq(t, r.Current(ctx).Source, "file")
	eq(t, len(asked), 0)
}

func TestMacReloginNoticedViaClaudeJSON(t *testing.T) {
	ctx := context.Background()
	h := t.TempDir()
	acct := filepath.Join(h, ".claude.json")
	writeAccount(t, acct)
	expiry := t0.Add(time.Minute)
	current := credsJSON(expiry)
	kc := func(context.Context, string) ([]byte, error) { return []byte(current), nil }
	clk := clock.NewFake(t0)
	r := NewReader(Options{Home: h, UseKeychain: true, Keychain: kc, Clock: clk})
	eq(t, r.Current(ctx).State(clk.Now()), "ok")
	clk.Set(expiry.Add(time.Second))
	eq(t, r.Current(ctx).State(clk.Now()), "expired") // the expiry read
	eq(t, r.Reads(), 2)
	current = credsJSON(clk.Now().Add(8 * time.Hour)) // signed in again; the keychain has no mtime
	clk.Advance(5 * time.Minute)
	touchLater(t, acct, time.Second)
	eq(t, r.Current(ctx).State(clk.Now()), "expired")
	eq(t, r.Reads(), 2)
	clk.Advance(6 * time.Minute) // 11 minutes after the last read
	eq(t, r.Current(ctx).State(clk.Now()), "ok")
	eq(t, r.Reads(), 3)
	// while ok, .claude.json changes do not cause reads
	touchLater(t, acct, 2*time.Second)
	clk.Advance(20 * time.Minute)
	r.Current(ctx)
	eq(t, r.Reads(), 3)
}

func TestAccountFileOverCapExplainsMissingAccount(t *testing.T) {
	old := maxAccountFile
	maxAccountFile = 4096
	t.Cleanup(func() { maxAccountFile = old })
	h := t.TempDir()
	writeCreds(t, filepath.Join(h, ".claude"), t0.Add(time.Hour))
	// oauthAccount sits after a large projects value, past the cap
	big := `{"projects":{"x":"` + strings.Repeat("a", 8192) + `"},"oauthAccount":{"accountUuid":"acc-1","emailAddress":"user-a@example.com"}}`
	must(t, os.WriteFile(filepath.Join(h, ".claude.json"), []byte(big), 0o600))
	l := NewReader(Options{Home: h, Clock: clock.NewFake(t0)}).Current(context.Background())
	eq(t, l.Account == nil, true)
	eq(t, l.State(t0), "ok")
	eq(t, l.Problem, "account file larger than 4 KiB")

	// the same layout under the cap is read in full
	maxAccountFile = 1 << 20
	l = NewReader(Options{Home: h, Clock: clock.NewFake(t0)}).Current(context.Background())
	eq(t, l.Account != nil && l.Account.UUID == "acc-1", true)
	eq(t, l.Problem, "")
}
