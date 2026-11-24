package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

func codexItem(at time.Time) backlog.Item {
	return backlog.Item{Provider: "codex", AccountKey: "codex:acc:user", FetchedAt: protocol.Time{Time: at}, HTTPStatus: 200,
		Usage: json.RawMessage(`{"p":3}`), Source: "wham"}
}

func standbyV2(next int, problems map[string]string) protocol.ResponseV2 {
	out := protocol.ResponseV2{NextSyncSeconds: next}
	for _, p := range []string{"claude", "codex"} {
		b := protocol.ProviderReply{Provider: p, Role: "standby", Peers: protocol.PeersV2{Devices: 1, PCs: 1}}
		if v, ok := problems[p]; ok {
			b.Problem = &v
		}
		out.Providers = append(out.Providers, b)
	}
	return out
}

// RF4: the server is still on schema 1 while the PC upgrades. The agent
// falls back at once (Claude only), keeps the Codex results, fetches Codex
// on the stand-alone schedule meanwhile, tries schema 2 again after an hour,
// and then delivers the Codex results once.
func TestFallbackToSchema1KeepsCodexBacklog(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	other := &fakeProvider{id: provider.Codex, clk: r.clk}
	r.extra = []provider.Provider{other}
	r.e = r.build()
	must(t, r.backlog.Add(codexItem(t0.Add(-10*time.Minute))))
	must(t, r.backlog.Add(backlog.Item{Provider: "claude", AccountKey: "acc-1", FetchedAt: protocol.Time{Time: t0.Add(-9 * time.Minute)}, HTTPStatus: 200}))
	upgraded := false
	var delivered []protocol.ResultV2
	v2at := []time.Time{}
	r.syncer.fn2 = func(_ int, _ string, req protocol.RequestV2) (protocol.ResponseV2, error) {
		v2at = append(v2at, r.clk.Now())
		if !upgraded {
			return protocol.ResponseV2{}, protocol.ErrUnsupportedSchema
		}
		for _, b := range req.Providers {
			if b.Provider == "codex" {
				delivered = append(delivered, b.Results...)
			}
		}
		return standbyV2(60, nil), nil
	}
	r.step()
	// the same step sent schema 1 right after the refusal
	eq(t, len(r.syncer.calls), 2)
	eq(t, r.syncer.calls[1].v2 == nil, true)
	eq(t, r.syncer.calls[1].req.Schema, 1)
	eq(t, len(r.syncer.calls[1].req.Results), 1) // Claude's only
	eq(t, r.snap().SchemaFallback, true)
	eq(t, r.snap().Mode, "coordinated")
	eq(t, len(r.backlog.Pending(10, "claude")), 0)
	eq(t, len(r.backlog.Pending(10, "codex")), 1)
	for r.clk.Now().Sub(t0) < 59*time.Minute {
		r.step()
	}
	eq(t, len(v2at), 1) // not tried again within the hour
	if len(other.at) != 2 {
		t.Fatalf("Codex fetched %d times in the hour of fallback, want 2 (stand-alone schedule)", len(other.at))
	}
	if gap := other.at[1].Sub(other.at[0]); gap < StandaloneMin || gap > StandaloneMax {
		t.Fatalf("gap %v", gap)
	}
	eq(t, len(r.backlog.Pending(10, "codex")), 3) // kept: schema 1 carries Claude only
	eq(t, r.cl().State, "ok")
	upgraded = true
	r.stepUntil(t, 5*time.Minute, func() bool { return len(v2at) >= 2 })
	eq(t, len(delivered), 3)
	eq(t, r.backlog.Len(), 0)
	eq(t, r.snap().SchemaFallback, false)
	r.step()
	eq(t, len(delivered), 3) // not sent twice
	n := len(other.at)
	for i := 0; i < 60; i++ {
		r.step()
	}
	eq(t, len(other.at), n) // served by the server again: Codex is a standby, no own fetches
}

// A block the server could not read keeps its results for the next sync;
// any other problem (or none) means they were stored.
func TestUnreadableBlockKeepsBacklog(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	r.extra = []provider.Provider{&fakeProvider{id: provider.Codex, clk: r.clk, state: "expired"}}
	r.e = r.build()
	must(t, r.backlog.Add(codexItem(t0.Add(-10*time.Minute))))
	must(t, r.backlog.Add(backlog.Item{Provider: "claude", AccountKey: "acc-1", FetchedAt: protocol.Time{Time: t0.Add(-9 * time.Minute)}, HTTPStatus: 200}))
	answers := []map[string]string{{"claude": "degraded_block", "codex": "unreadable_block"}, {"codex": "unknown_auth_state"}}
	sent := map[string]int{}
	r.syncer.fn2 = func(n int, _ string, req protocol.RequestV2) (protocol.ResponseV2, error) {
		for _, b := range req.Providers {
			sent[b.Provider] += len(b.Results)
		}
		return standbyV2(60, answers[min(n, len(answers)-1)]), nil
	}
	r.step()
	eq(t, len(r.backlog.Pending(10, "claude")), 0)
	eq(t, len(r.backlog.Pending(10, "codex")), 1)
	eq(t, r.snap().Providers[provider.Codex].ServerProblem, "unreadable_block")
	r.step()
	eq(t, sent, map[string]int{"claude": 1, "codex": 2})
	eq(t, r.backlog.Len(), 0)
	eq(t, r.snap().Providers[provider.Codex].ServerProblem, "unknown_auth_state")
}

// Lanes are independent in coordinated mode too: both providers are told
// to fetch on every sync; one answering 429 does not hold the other back.
func TestLanesAreIndependentWhenTheServerOrders(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	other := &fakeProvider{id: provider.Codex, clk: r.clk}
	r.extra = []provider.Provider{other}
	r.e = r.build()
	r.fetcher.status = 429
	r.syncer.fn2 = func(int, string, protocol.RequestV2) (protocol.ResponseV2, error) {
		x := standbyV2(60, nil)
		for i := range x.Providers {
			x.Providers[i].Role, x.Providers[i].FetchNow = "poller", true
		}
		return x, nil
	}
	for r.clk.Now().Sub(t0) < time.Hour {
		r.step()
	}
	if len(other.at) != 15 || r.fetcher.calls < 3 || r.fetcher.calls > 8 {
		t.Fatalf("codex %d fetches, claude %d", len(other.at), r.fetcher.calls)
	}
}

// --- every schema 2 (and the mixed schema 1) vector request is what this
// engine builds for that state ---

type vecStep struct {
	Request struct {
		Body json.RawMessage `json:"body"`
	} `json:"request"`
}

type vecFile struct {
	Name  string    `json:"name"`
	Steps []vecStep `json:"steps"`
}

// vecProvider signs in as one block of a vector says.
type vecProvider struct {
	id    provider.ID
	login provider.Login
}

func (v *vecProvider) ID() provider.ID                      { return v.id }
func (v *vecProvider) Login(context.Context) provider.Login { return v.login }
func (v *vecProvider) Fetch(context.Context) (provider.Result, error) {
	return provider.Result{}, fmt.Errorf("no fetch")
}
func (v *vecProvider) Parse(json.RawMessage) provider.Parsed { return provider.Parsed{} }

// sameBody: equal, a key the vector leaves out must be null here.
func sameBody(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			if !sameBody(wv, g[k]) {
				return false
			}
		}
		for k, gv := range g {
			if _, ok := w[k]; !ok && gv != nil {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !sameBody(w[i], g[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}

// byTime orders every "results" list by fetched_at: the agent sends its
// backlog oldest first, a vector may list a batch in any order.
func byTime(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = byTime(e)
		}
		if rs, ok := x["results"].([]any); ok {
			sort.SliceStable(rs, func(i, j int) bool {
				a, _ := rs[i].(map[string]any)
				b, _ := rs[j].(map[string]any)
				return fmt.Sprint(a["fetched_at"]) < fmt.Sprint(b["fetched_at"])
			})
		}
	case []any:
		for i := range x {
			x[i] = byTime(x[i])
		}
	}
	return v
}

func generic(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	must(t, json.Unmarshal(b, &v))
	return v
}

func TestEngineBuildsTheVectorRequests(t *testing.T) {
	files, err := filepath.Glob("../testdata/vectors/*.json")
	must(t, err)
	sort.Strings(files)
	built := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		must(t, err)
		var v vecFile
		must(t, json.Unmarshal(raw, &v))
		for i, s := range v.Steps {
			name := fmt.Sprintf("%s/%d", v.Name, i)
			var head struct {
				Schema  int     `json:"schema"`
				Event   *string `json:"event"`
				Machine *struct {
					Key, Source string
				} `json:"machine"`
			}
			must(t, json.Unmarshal(s.Request.Body, &head))
			if head.Schema != 2 && head.Schema != 1 || name == "18-unreadable-parts-degrade/0" || name == "20-degraded-block-keeps-results/0" ||
				name == "18-unreadable-parts-degrade/1" { // 18/1 still carries an event this agent never sends
				continue
			}
			var req protocol.RequestV2
			var req1 protocol.Request
			if head.Schema == 2 {
				must(t, json.Unmarshal(s.Request.Body, &req))
				// "Refresh now" asks for every provider at once: a refresh
				// for one block only is not a request this agent sends
				mixed := false
				for _, b := range req.Providers {
					mixed = mixed || b.WantFresh != req.Providers[0].WantFresh
				}
				if mixed {
					continue
				}
			} else {
				must(t, json.Unmarshal(s.Request.Body, &req1))
				req = protocol.RequestV2{Client: req1.Client, Device: req1.Device, Providers: []protocol.ProviderBlock{{Provider: "claude",
					Auth: protocol.AuthV2{State: req1.Auth.State, TokenExpiresAt: req1.Auth.TokenExpiresAt}, WantFresh: req1.WantFresh}}}
				if a := req1.Account; a != nil {
					req.Providers[0].Account = &protocol.AccountV2{Key: a.UUID, Email: a.Email, DisplayName: a.DisplayName, OrganizationName: a.OrganizationName,
						OrganizationType: a.OrganizationType, SubscriptionType: a.SubscriptionType, RateLimitTier: a.RateLimitTier}
				}
				for _, x := range req1.Results {
					req.Providers[0].Results = append(req.Providers[0].Results, protocol.ResultV2{AccountKey: x.AccountUUID, FetchedAt: x.FetchedAt, HTTPStatus: x.HTTPStatus, Usage: x.Usage})
				}
			}
			id := Identity{DeviceID: req.Device.ID, Hostname: string(req.Device.Hostname), OSUser: string(req.Device.OSUser), OS: req.Client.OS,
				OSVersion: string(req.Client.OSVersion), Arch: string(req.Client.Arch), Version: req.Client.Version, Mode: string(req.Client.Mode)}
			if head.Machine != nil {
				id.MachineKey, id.MachineSource = head.Machine.Key, head.Machine.Source
			}
			var got []byte
			if head.Event != nil && *head.Event == protocol.EventUninstall {
				got, err = json.Marshal(UninstallRequest(id))
				must(t, err)
			} else {
				dir := t.TempDir()
				bl, err := backlog.Open(filepath.Join(dir, "backlog.json"))
				must(t, err)
				var providers []provider.Provider
				fresh := false
				for _, b := range req.Providers {
					l := provider.Login{Provider: provider.ID(b.Provider), State: b.Auth.State}
					if b.Auth.TokenExpiresAt != nil {
						l.ExpiresAt = b.Auth.TokenExpiresAt.Time
					}
					if a := b.Account; a != nil {
						l.AccountKey, l.Email, l.DisplayName = a.Key, string(a.Email), string(a.DisplayName)
						l.OrganizationName, l.OrganizationType, l.RateLimitTier = string(a.OrganizationName), string(a.OrganizationType), string(a.RateLimitTier)
						l.Plan = string(a.SubscriptionType)
						if b.Provider == "codex" {
							l.Plan = string(a.PlanType)
						}
					}
					providers = append(providers, &vecProvider{id: provider.ID(b.Provider), login: l})
					for _, x := range b.Results {
						it := backlog.Item{Provider: b.Provider, AccountKey: x.AccountKey, FetchedAt: x.FetchedAt, HTTPStatus: x.HTTPStatus, Usage: x.Usage,
							Source: string(x.Source), CLIVersion: string(x.CLIVersion), Detail: string(x.Detail), RetryAfterSeconds: x.RetryAfterSeconds}
						must(t, bl.Add(it))
					}
					fresh = fresh || b.WantFresh
				}
				e := New(Deps{Clock: clock.NewFake(t0), Providers: providers, Backlog: bl, Identity: id, Syncer: &fakeSyncer{}})
				for _, l := range e.lanes {
					l.login = l.p.Login(ctx)
				}
				if head.Schema == 1 {
					r1, _ := e.requestV1(t0)
					r1.WantFresh = fresh
					got, err = json.Marshal(r1)
				} else {
					r2, _ := e.requestV2(fresh)
					got, err = json.Marshal(r2)
				}
				must(t, err)
			}
			if !sameBody(byTime(generic(t, s.Request.Body)), byTime(generic(t, got))) {
				t.Errorf("%s:\n built  %s\n vector %s", name, got, s.Request.Body)
			}
			built++
		}
	}
	if built < 25 {
		t.Fatalf("only %d vector requests built", built)
	}
}
