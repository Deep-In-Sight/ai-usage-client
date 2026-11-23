package creds

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fakeToken = "fake-access-token-0123456789"

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

func credsJSON(expires time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"fake-refresh","expiresAt":%d,"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`,
		fakeToken, expires.UnixMilli())
}

// writeCreds writes <dir>/.credentials.json.
func writeCreds(t *testing.T, dir string, expires time.Time) string {
	t.Helper()
	must(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, ".credentials.json")
	must(t, os.WriteFile(p, []byte(credsJSON(expires)), 0o600))
	return p
}

func writeAccount(t *testing.T, path string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(`{"oauthAccount":{"accountUuid":"acc-1","emailAddress":"user-a@example.com","displayName":"User A","organizationName":"Example","organizationType":"claude_max"},"projects":{"x":1}}`), 0o600))
}

// touchLater moves the file's modification time forward.
func touchLater(t *testing.T, p string, by time.Duration) {
	t.Helper()
	st, err := os.Stat(p)
	must(t, err)
	m := st.ModTime().Add(by)
	must(t, os.Chtimes(p, m, m))
}

func hex8(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func TestKeychainService(t *testing.T) {
	eq(t, KeychainService(""), "Claude Code-credentials")
	eq(t, KeychainService("/x/claude"), "Claude Code-credentials-"+hex8("/x/claude"))
}

func TestLoginState(t *testing.T) {
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	eq(t, Login{}.State(now), "not_signed_in")
	eq(t, Login{HasToken: true}.State(now), "ok")
	eq(t, Login{HasToken: true, ExpiresAt: now}.State(now), "expired")
	eq(t, Login{HasToken: true, ExpiresAt: now.Add(time.Second)}.State(now), "ok")
}

func TestErrorTextsNeverContainContent(t *testing.T) {
	_, _, err := parseCreds([]byte(`{"claudeAiOauth":{"accessToken":"` + fakeToken + `",`))
	if err == nil || strings.Contains(err.Error(), "fake-access") {
		t.Fatalf("%v", err)
	}
	_, _, err = parseCreds([]byte(`{"claudeAiOauth":{"accessToken":""}}`))
	if err == nil {
		t.Fatal("empty token should fail")
	}
}
