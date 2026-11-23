// Package status is the agent's status file (status.json), written after
// every engine step. The status command, and later the tray, read it. It
// holds no token and no ingest key.
package status

import (
	"encoding/json"
	"os"
	"time"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/atomicfile"
)

// Snapshot is the agent's current state.
type Snapshot struct {
	Version             string    `json:"version"`
	PID                 int       `json:"pid"`
	UpdatedAt           time.Time `json:"updated_at,omitzero"`
	Mode                string    `json:"mode"` // coordinated | standalone | starting
	Role                string    `json:"role,omitempty"`
	ServerURL           string    `json:"server_url,omitempty"`
	LastSyncAt          time.Time `json:"last_sync_at,omitzero"`
	LastSyncOK          bool      `json:"last_sync_ok"`
	SyncFailures        int       `json:"sync_failures"`
	SyncProblem         string    `json:"sync_problem,omitempty"`
	LastFetchAt         time.Time `json:"last_fetch_at,omitzero"`
	LastFetchStatus     *int      `json:"last_fetch_status,omitempty"`
	NextStandaloneFetch time.Time `json:"next_standalone_fetch,omitzero"`
	AuthState           string    `json:"auth_state"`
	TokenExpiresAt      time.Time `json:"token_expires_at,omitzero"`
	AccountEmail        string    `json:"account_email,omitempty"`
	LatestFetchedAt     time.Time `json:"latest_fetched_at,omitzero"`
	WeeklyPercent       *float64  `json:"weekly_percent,omitempty"`
	ScopedPercent       *float64  `json:"scoped_percent,omitempty"`
	WeeklyResetsAt      time.Time `json:"weekly_resets_at,omitzero"`
	Backlog             int       `json:"backlog"`
	PeersDevices        int       `json:"peers_devices"`
	PollerHostname      string    `json:"poller_hostname,omitempty"`
	ClientLatestVersion string    `json:"client_latest_version,omitempty"`
}

// Write replaces the status file.
func Write(path string, s Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// Read loads the status file.
func Read(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
