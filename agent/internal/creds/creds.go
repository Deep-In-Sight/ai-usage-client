// Package creds reads the Claude Code sign-in: access token, expiry, plan
// and the account from .claude.json. It only reads; it never refreshes or
// rewrites anything.
//
// The token never leaves this package except as the argument of the
// callback given to Reader.WithToken. Login, the type other packages see,
// has no token field.
package creds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"
)

// Account is the signed-in account from .claude.json (oauthAccount).
type Account struct {
	UUID, Email, DisplayName, OrganizationName, OrganizationType string
}

// Login is what the agent knows about the sign-in, without the token.
type Login struct {
	HasToken         bool
	ExpiresAt        time.Time // zero: unknown
	SubscriptionType string
	RateLimitTier    string
	Account          *Account
	Source           string // "file", "keychain" or ""
	Problem          string // why there is no usable sign-in; never file content
}

// State is "not_signed_in", "expired" or "ok".
func (l Login) State(now time.Time) string {
	switch {
	case !l.HasToken:
		return "not_signed_in"
	case !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt):
		return "expired"
	}
	return "ok"
}

var (
	ErrNotSignedIn = errors.New("not signed in")
	ErrExpired     = errors.New("sign-in expired")
	ErrNoKeychain  = errors.New("keychain: not available on this OS")
)

// KeychainFunc returns the secret stored under service.
type KeychainFunc func(ctx context.Context, service string) ([]byte, error)

// KeychainService is the macOS keychain item Claude Code uses. With a
// CLAUDE_CONFIG_DIR the item name carries a short hash of that folder.
// (The name is not documented; config.json's keychain_service overrides it.)
func KeychainService(claudeConfigDir string) string {
	if claudeConfigDir == "" {
		return "Claude Code-credentials"
	}
	h := sha256.Sum256([]byte(claudeConfigDir))
	return "Claude Code-credentials-" + hex.EncodeToString(h[:])[:8]
}

type rawCreds struct {
	ClaudeAiOauth *struct {
		AccessToken      string          `json:"accessToken"`
		ExpiresAt        json.RawMessage `json:"expiresAt"` // read leniently: an odd value is an unknown expiry
		SubscriptionType string          `json:"subscriptionType"`
		RateLimitTier    string          `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

var (
	errNotJSON = errors.New("credentials file is not valid JSON")
	errNoToken = errors.New("no access token in the sign-in")
)

// parseCreds decodes the credentials JSON. The token is returned separately
// so that the caller can drop it; errors never include content.
func parseCreds(data []byte) (Login, string, error) {
	var r rawCreds
	if err := json.Unmarshal(data, &r); err != nil {
		return Login{}, "", errNotJSON
	}
	o := r.ClaudeAiOauth
	if o == nil || o.AccessToken == "" {
		return Login{Problem: errNoToken.Error()}, "", errNoToken
	}
	l := Login{HasToken: true, SubscriptionType: o.SubscriptionType, RateLimitTier: o.RateLimitTier}
	if ms, err := strconv.ParseFloat(string(o.ExpiresAt), 64); err == nil && ms > 0 && ms < 1e15 {
		l.ExpiresAt = time.UnixMilli(int64(ms)).UTC()
	}
	return l, o.AccessToken, nil
}

type rawAccountFile struct {
	OAuthAccount *struct {
		AccountUUID      string `json:"accountUuid"`
		EmailAddress     string `json:"emailAddress"`
		DisplayName      string `json:"displayName"`
		OrganizationName string `json:"organizationName"`
		OrganizationType string `json:"organizationType"`
	} `json:"oauthAccount"`
}

// parseAccountFrom stream-decodes only the top-level oauthAccount key of
// .claude.json (which can be many MB of project history).
func parseAccountFrom(rd io.Reader) *Account {
	dec := json.NewDecoder(rd)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var r rawAccountFile
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		if key, _ := tok.(string); key == "oauthAccount" {
			if dec.Decode(&r.OAuthAccount) != nil {
				return nil
			}
			break
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return nil
		}
	}
	if r.OAuthAccount == nil || r.OAuthAccount.AccountUUID == "" {
		return nil
	}
	a := r.OAuthAccount
	return &Account{UUID: a.AccountUUID, Email: a.EmailAddress, DisplayName: a.DisplayName,
		OrganizationName: a.OrganizationName, OrganizationType: a.OrganizationType}
}
