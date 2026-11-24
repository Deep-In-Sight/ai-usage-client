package protocol

import "encoding/json"

// Schema 2 (design D9, 6.8): the device once, then one block per provider
// with its account, sign-in state, results and refresh request; the reply
// is per provider too.

// SchemaV2 is the protocol version with provider blocks.
const SchemaV2 = 2

// EventUninstall is the event of the last sync before removal.
const EventUninstall = "uninstall"

type RequestV2 struct {
	Schema    int             `json:"schema"`
	Client    ClientInfo      `json:"client"`
	Device    DeviceInfo      `json:"device"`
	Machine   *MachineInfo    `json:"machine"`   // null when there is no machine key
	Event     *string         `json:"event"`     // null or "uninstall"
	Providers []ProviderBlock `json:"providers"` // never null
}

// MarshalJSON writes nil Providers as [].
func (r RequestV2) MarshalJSON() ([]byte, error) {
	type plain RequestV2
	if r.Providers == nil {
		r.Providers = []ProviderBlock{}
	}
	return json.Marshal(plain(r))
}

// MachineInfo is the hashed machine key (never the machine id itself).
type MachineInfo struct {
	Key    string `json:"key"`
	Source string `json:"source"` // "os": the only source this agent sends
}

type ProviderBlock struct {
	Provider     string     `json:"provider"`
	Account      *AccountV2 `json:"account"`
	Auth         AuthV2     `json:"auth"`
	ParseProblem Opt        `json:"parse_problem"`
	Results      []ResultV2 `json:"results"` // never null
	WantFresh    bool       `json:"want_fresh"`
}

// MarshalJSON writes nil Results as [].
func (b ProviderBlock) MarshalJSON() ([]byte, error) {
	type plain ProviderBlock
	if b.Results == nil {
		b.Results = []ResultV2{}
	}
	return json.Marshal(plain(b))
}

type AccountV2 struct {
	Key              string `json:"key"`
	Email            Opt    `json:"email"`
	DisplayName      Opt    `json:"display_name"`
	OrganizationName Opt    `json:"organization_name"`
	OrganizationType Opt    `json:"organization_type"`
	SubscriptionType Opt    `json:"subscription_type"` // Claude's plan
	RateLimitTier    Opt    `json:"rate_limit_tier"`
	PlanType         Opt    `json:"plan_type"` // Codex's plan
}

type AuthV2 struct {
	State          string `json:"state"`
	TokenExpiresAt *Time  `json:"token_expires_at"`
}

type ResultV2 struct {
	AccountKey        string          `json:"account_key"`
	FetchedAt         Time            `json:"fetched_at"`
	HTTPStatus        int             `json:"http_status"`
	Usage             json.RawMessage `json:"usage"` // nil is sent as null
	Source            Opt             `json:"source"`
	CLIVersion        Opt             `json:"cli_version"`
	Detail            Opt             `json:"detail"`
	RetryAfterSeconds *int            `json:"retry_after_seconds"`
}

type ResponseV2 struct {
	Providers           []ProviderReply `json:"providers"`
	NextSyncSeconds     int             `json:"next_sync_seconds"`
	ServerURLs          []string        `json:"server_urls"`
	ClientLatestVersion *string         `json:"client_latest_version"`
}

type ProviderReply struct {
	Provider string    `json:"provider"`
	Role     string    `json:"role"`
	FetchNow bool      `json:"fetch_now"`
	Latest   *LatestV2 `json:"latest"`
	Peers    PeersV2   `json:"peers"`
	// Problem: null normally; "unreadable_block" means the block's results
	// were not stored (keep and send them again); any other value means
	// they were stored.
	Problem *string `json:"problem"`
}

// ProblemUnreadableBlock is the only problem that means "not stored".
const ProblemUnreadableBlock = "unreadable_block"

type LatestV2 struct {
	FetchedAt string          `json:"fetched_at"`
	Source    *string         `json:"source"`
	Usage     json.RawMessage `json:"usage"`
}

type PeersV2 struct {
	Devices        int     `json:"devices"`
	PCs            int     `json:"pcs"`
	PollerHostname *string `json:"poller_hostname"`
}
