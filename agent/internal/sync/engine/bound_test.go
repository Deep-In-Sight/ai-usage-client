package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

// hangProvider is a provider whose service never answers: its fetch ends
// only with its context.
type hangProvider struct {
	fakeProvider
	calls int
}

func (h *hangProvider) Fetch(ctx context.Context) (provider.Result, error) {
	h.calls++
	at := h.clk.Now()
	<-ctx.Done()
	return provider.Result{Provider: h.id, FetchedAt: at.UTC().Truncate(time.Millisecond), Problem: "usage request failed: timed out"}, nil
}

// pollBoth answers every schema 2 sync: this PC fetches both providers now.
func pollBoth(int, string, protocol.RequestV2) (protocol.ResponseV2, error) {
	var ps []protocol.ProviderReply
	for _, p := range []string{"claude", "codex"} {
		ps = append(ps, protocol.ProviderReply{Provider: p, Role: "poller", FetchNow: true, Peers: protocol.PeersV2{Devices: 1, PCs: 1}})
	}
	return protocol.ResponseV2{Providers: ps, NextSyncSeconds: 60}, nil
}

// A provider whose service hangs delays the other provider's result by
// its fetch budget at most, whichever lane comes first: the server gets
// both in the sync right after the fetches, within its 90 s fetch timeout
// (the lanes fetch one after the other; review stage 1).
func TestAHangingProviderDelaysTheOtherOnlyByItsBudget(t *testing.T) {
	for _, codexFirst := range []bool{false, true} {
		r := newRig(t, nil)
		r.syncer.fn2 = pollBoth
		hang := &hangProvider{fakeProvider: fakeProvider{id: provider.Codex, clk: r.clk}}
		r.extra = []provider.Provider{hang}
		r.codexFirst = codexFirst
		r.fetchTimeout = 150 * time.Millisecond
		r.e = r.build()
		start := time.Now()
		r.e.Step(ctx)
		took := time.Since(start)
		if took < 150*time.Millisecond || took > 150*time.Millisecond+3*time.Second {
			t.Fatalf("codex first %v: the step took %v", codexFirst, took)
		}
		eq(t, hang.calls, 1)
		eq(t, r.fetcher.calls, 1)
		calls := r.syncer.calls
		eq(t, len(calls), 2) // the order, then the results
		got := map[string]int{}
		for _, b := range calls[1].v2.Providers {
			for _, res := range b.Results {
				got[b.Provider] = res.HTTPStatus
			}
		}
		eq(t, got, map[string]int{"claude": 200, "codex": 0})
	}
}

// The budget: Claude's and Codex's worst fetch plus one sync stay under
// the server's 90 s fetch timeout (the server hands over to another PC
// after it), with the engine's cap on every fetch.
func TestWorstCaseFitsTheServersFetchTimeout(t *testing.T) {
	const serverFetchTimeout = 90 * time.Second
	if worst := 2*FetchTimeout + protocol.SyncTimeout; worst >= serverFetchTimeout {
		t.Fatalf("two fetches and a sync can take %v", worst)
	}
}
