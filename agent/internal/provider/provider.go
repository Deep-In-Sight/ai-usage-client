// Package provider is what the engine knows about one AI service whose plan
// usage the agent reads (Claude, Codex): the sign-in without its token, one
// fetch, and how to read a reply into rows. Tokens stay inside each
// provider's own package; nothing here carries one.
package provider

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// ID names a provider; it is the provider value of the sync protocol.
type ID string

const (
	Claude ID = "claude"
	Codex  ID = "codex"
)

// Name is the provider's name as the menu and status show it.
func (id ID) Name() string {
	switch id {
	case Claude:
		return "Claude"
	case Codex:
		return "Codex"
	}
	return string(id)
}

// Login is what the agent knows about one provider's sign-in. No token.
type Login struct {
	Provider ID
	// State: ok | expired | not_signed_in | unsupported_store | api_key |
	// unsupported_base_url
	State      string
	AccountKey string // Claude: the account UUID; Codex: "codex:<account_id>:<user_id>"

	Email, DisplayName, OrganizationName, OrganizationType, Plan, RateLimitTier string

	ExpiresAt time.Time // zero: unknown
	Problem   string    // why it is not usable; never file content

	// The fallback's executable as the status command shows it
	// ("<path> <version>"; Codex only) and why it cannot be used, if so.
	// Local status only: never sent to the server.
	FallbackCLI, FallbackProblem string
}

// Result is one fetch attempt.
type Result struct {
	Provider   ID
	AccountKey string
	FetchedAt  time.Time       // UTC, milliseconds
	HTTPStatus int             // 0: no response
	Usage      json.RawMessage // only for 200 with a JSON object body
	RetryAfter time.Duration
	Source     string // "" (Claude), "wham", "app-server"
	CLIVersion string
	Detail     string // at most 200 bytes, no token
	Problem    string
}

// Row is one limit as the menu, the icon and status show it.
type Row struct {
	Key        string      `json:"key"`   // "five_hour", "weekly_all", "weekly_scoped:Fable", "codex:weekly", …
	Label      string      `json:"label"` // "5-hour", "Weekly", "Fable", "30-day", "<name> · Weekly"
	Percent    *float64    `json:"percent,omitempty"`
	ResetsAt   *time.Time  `json:"resets_at,omitempty"`
	Level      usage.Level `json:"level"`
	Blocked    bool        `json:"blocked,omitempty"`
	WindowMins int         `json:"window_mins,omitempty"`
}

// Parsed is a reply read into rows.
type Parsed struct {
	Rows         []Row  // display order: shorter windows first, model/family rows last
	MainKey      string // the statistics main limit (design D7); "" if none
	ParseProblem string // "" or "no_weekly"
}

// Row finds a row by its key.
func (p Parsed) Row(key string) (Row, bool) {
	for _, r := range p.Rows {
		if r.Key == key {
			return r, true
		}
	}
	return Row{}, false
}

// Provider is one AI service the agent reads usage from.
type Provider interface {
	ID() ID
	// Login is the current sign-in; it reads the sign-in files only when
	// the base spec's rule (4.3) says to.
	Login(ctx context.Context) Login
	// Fetch asks the service for the usage once. It returns an error only
	// when no request was attempted.
	Fetch(ctx context.Context) (Result, error)
	// Parse reads a reply the service (or the server) gave.
	Parse(raw json.RawMessage) Parsed
}

// Shown is the row the tray icon shows (design D7): the main row, unless
// another row is at 100 % or more (that limit stops the user now). With no
// main row the first row stands in for it.
func Shown(p Parsed) (Row, bool) {
	if len(p.Rows) == 0 {
		return Row{}, false
	}
	main, ok := p.Row(p.MainKey)
	if !ok {
		main = p.Rows[0]
	}
	if main.Percent != nil && *main.Percent >= 100 {
		return main, true
	}
	for _, r := range p.Rows {
		if r.Key != main.Key && r.Percent != nil && *r.Percent >= 100 {
			return r, true
		}
	}
	return main, true
}
