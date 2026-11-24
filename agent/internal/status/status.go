// Package status is the agent's status file (status.json), written after
// every engine step. The status command and the tray read it. It
// holds no token and no ingest key.
package status

import (
	"encoding/json"
	"os"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/atomicfile"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Snapshot is the agent's current state: what is common to all providers
// at the top, each provider's sign-in, role and numbers under Providers.
type Snapshot struct {
	Version             string    `json:"version"`
	PID                 int       `json:"pid"`
	UpdatedAt           time.Time `json:"updated_at,omitzero"`
	Mode                string    `json:"mode"` // coordinated | standalone | starting
	ServerURL           string    `json:"server_url,omitempty"`
	LastSyncAt          time.Time `json:"last_sync_at,omitzero"`
	LastSyncOK          bool      `json:"last_sync_ok"`
	SyncFailures        int       `json:"sync_failures"`
	SyncProblem         string    `json:"sync_problem,omitempty"`
	NextStandaloneFetch time.Time `json:"next_standalone_fetch,omitzero"`
	ClientMode          string    `json:"client_mode"`   // tray | headless: what this process shows
	FreshPending        bool      `json:"fresh_pending"` // a refresh was asked for and its follow-up syncs are not over
	Backlog             int       `json:"backlog"`
	ClientLatestVersion string    `json:"client_latest_version,omitempty"`
	SchemaFallback      bool      `json:"schema_fallback,omitempty"` // the server does not speak schema 2: schema 1, Claude only

	Providers map[provider.ID]ProviderStatus `json:"providers"`
}

// ProviderStatus is one provider's part of the snapshot.
type ProviderStatus struct {
	State           string         `json:"state"`             // provider.Login.State
	Problem         string         `json:"problem,omitempty"` // why the sign-in or account is missing; never file content
	AccountEmail    string         `json:"account_email,omitempty"`
	Plan            string         `json:"plan,omitempty"`
	ExpiresAt       time.Time      `json:"expires_at,omitzero"`
	Role            string         `json:"role,omitempty"` // poller | standby
	PollerHostname  string         `json:"poller_hostname,omitempty"`
	PeersPCs        int            `json:"peers_pcs"`
	LatestFetchedAt time.Time      `json:"latest_fetched_at,omitzero"`
	LatestSource    string         `json:"latest_source,omitempty"`
	ParseProblem    string         `json:"parse_problem,omitempty"` // the shown reply could not be read ("no_weekly")
	Rows            []provider.Row `json:"rows,omitempty"`
	MainKey         string         `json:"main_key,omitempty"`
	LastFetchAt     time.Time      `json:"last_fetch_at,omitzero"`
	LastFetchStatus *int           `json:"last_fetch_status,omitempty"`
	FreshPending    bool           `json:"fresh_pending,omitempty"`
	ServerProblem   string         `json:"server_problem,omitempty"` // the server's problem for this provider's block
}

// Provider returns p's part (the zero value when there is none).
func (s Snapshot) Provider(p provider.ID) ProviderStatus { return s.Providers[p] }

// Row finds one of the provider's rows by key.
func (ps ProviderStatus) Row(key string) (provider.Row, bool) {
	return provider.Parsed{Rows: ps.Rows}.Row(key)
}

// Write replaces the status file.
func Write(path string, s Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// Read loads the status file. A file a v0.7 or v0.8 agent wrote (Claude's
// numbers at the top) reads as Providers["claude"].
func Read(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, err
	}
	if s.Providers == nil {
		var old v08
		if err := json.Unmarshal(b, &old); err == nil && old.AuthState != "" {
			s.Providers = map[provider.ID]ProviderStatus{provider.Claude: old.claude()}
		}
	}
	return s, nil
}

// v08 is the Claude part of a v0.7/v0.8 status file.
type v08 struct {
	Role            string    `json:"role"`
	LastFetchAt     time.Time `json:"last_fetch_at"`
	LastFetchStatus *int      `json:"last_fetch_status"`
	AuthState       string    `json:"auth_state"`
	SignInProblem   string    `json:"sign_in_problem"`
	TokenExpiresAt  time.Time `json:"token_expires_at"`
	AccountEmail    string    `json:"account_email"`
	LatestFetchedAt time.Time `json:"latest_fetched_at"`
	WeeklyPercent   *float64  `json:"weekly_percent"`
	ScopedPercent   *float64  `json:"scoped_percent"`
	WeeklyResetsAt  time.Time `json:"weekly_resets_at"`
	WeeklySeverity  string    `json:"weekly_severity"`
	ScopedResetsAt  time.Time `json:"scoped_resets_at"`
	ScopedSeverity  string    `json:"scoped_severity"`
	FreshPending    bool      `json:"fresh_pending"`
	PeersDevices    int       `json:"peers_devices"`
	PollerHostname  string    `json:"poller_hostname"`
}

func (o v08) claude() ProviderStatus {
	row := func(key, label string, pct *float64, resets time.Time, sev string, mins int) provider.Row {
		if sev == "" {
			sev = "normal" // an older file without severity
		}
		r := provider.Row{Key: key, Label: label, Percent: pct, Level: usage.LevelOf(usage.Limit{Percent: pct, Severity: sev}), WindowMins: mins}
		if !resets.IsZero() {
			r.ResetsAt = &resets
		}
		return r
	}
	rows := []provider.Row{row("weekly_all", "Weekly", o.WeeklyPercent, o.WeeklyResetsAt, o.WeeklySeverity, 10080)}
	if o.ScopedPercent != nil {
		rows = append(rows, row("weekly_scoped:"+usage.ScopedModel, usage.ScopedModel, o.ScopedPercent, o.ScopedResetsAt, o.ScopedSeverity, 10080))
	}
	return ProviderStatus{State: o.AuthState, Problem: o.SignInProblem, AccountEmail: o.AccountEmail, ExpiresAt: o.TokenExpiresAt,
		Role: o.Role, PollerHostname: o.PollerHostname, PeersPCs: o.PeersDevices, LatestFetchedAt: o.LatestFetchedAt,
		Rows: rows, MainKey: "weekly_all", LastFetchAt: o.LastFetchAt, LastFetchStatus: o.LastFetchStatus, FreshPending: o.FreshPending}
}
