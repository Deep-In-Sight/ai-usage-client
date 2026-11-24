// Package protocol is the wire format of POST /api/v1/sync (schema 1) and a
// client for it. It knows nothing about the Claude sign-in: no token can
// reach a sync request through this package.
package protocol

import (
	"encoding/json"
	"errors"
	"time"
)

// Schema is the protocol version this agent speaks.
const Schema = 1

const timeLayout = "2006-01-02T15:04:05.000Z"

// Time is a UTC timestamp with milliseconds, never null.
type Time struct{ time.Time }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(timeLayout))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == nil {
		return errors.New("protocol: time must not be null")
	}
	v, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return errors.New("protocol: bad time")
	}
	t.Time = v.UTC()
	return nil
}

// Opt is an optional string: "" is sent as null and null reads as "".
type Opt string

func (o Opt) MarshalJSON() ([]byte, error) {
	if o == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(o))
}

func (o *Opt) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == nil {
		*o = ""
	} else {
		*o = Opt(*s)
	}
	return nil
}

type Request struct {
	Schema    int          `json:"schema"`
	Client    ClientInfo   `json:"client"`
	Device    DeviceInfo   `json:"device"`
	Account   *AccountInfo `json:"account"`
	Auth      AuthInfo     `json:"auth"`
	Results   []Result     `json:"results"`
	WantFresh bool         `json:"want_fresh"`
}

// MarshalJSON writes nil Results as [].
func (r Request) MarshalJSON() ([]byte, error) {
	type plain Request
	if r.Results == nil {
		r.Results = []Result{}
	}
	return json.Marshal(plain(r))
}

type ClientInfo struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	OSVersion Opt    `json:"os_version"`
	Arch      Opt    `json:"arch"`
	Mode      Opt    `json:"mode"`
}

type DeviceInfo struct {
	ID       string `json:"id"`
	Hostname Opt    `json:"hostname"`
	OSUser   Opt    `json:"os_user"`
}

type AccountInfo struct {
	UUID             string `json:"uuid"`
	Email            Opt    `json:"email"`
	DisplayName      Opt    `json:"display_name"`
	OrganizationName Opt    `json:"organization_name"`
	OrganizationType Opt    `json:"organization_type"`
	SubscriptionType Opt    `json:"subscription_type"`
	RateLimitTier    Opt    `json:"rate_limit_tier"`
}

type AuthInfo struct {
	State          string `json:"state"` // ok | expired | not_signed_in
	TokenExpiresAt *Time  `json:"token_expires_at"`
}

// Result is one fetch result as reported to the server.
type Result struct {
	AccountUUID string          `json:"account_uuid"`
	FetchedAt   Time            `json:"fetched_at"`
	HTTPStatus  int             `json:"http_status"`
	Usage       json.RawMessage `json:"usage"` // nil is sent as null
}

type Response struct {
	Role                string   `json:"role"`
	FetchNow            bool     `json:"fetch_now"`
	NextSyncSeconds     int      `json:"next_sync_seconds"`
	Latest              *Latest  `json:"latest"`
	Peers               Peers    `json:"peers"`
	ServerURLs          []string `json:"server_urls"`
	ClientLatestVersion *string  `json:"client_latest_version"`
}

type Latest struct {
	FetchedAt string          `json:"fetched_at"`
	Source    string          `json:"source,omitempty"` // schema 2: "wham", "app-server"; "" for Claude
	Usage     json.RawMessage `json:"usage"`
}

type Peers struct {
	Devices        int     `json:"devices"`
	PollerHostname *string `json:"poller_hostname"`
}
