package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

func withLatest(r protocol.Response, fetchedAt string) protocol.Response {
	r.Latest = &protocol.Latest{FetchedAt: fetchedAt, Usage: json.RawMessage(usageBody)}
	return r
}

func TestRequestFreshSendsWantFreshOnce(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	r.e.RequestFresh()
	r.step()
	r.step()
	reqs := r.syncer.reqs()
	eq(t, reqs[0].WantFresh, true)
	eq(t, reqs[1].WantFresh, false)
}

func TestRequestFreshKeptUntilASyncSucceeds(t *testing.T) {
	fail := true
	r := newRig(t, func(int, string, protocol.Request) (protocol.Response, error) {
		if fail {
			return protocol.Response{}, errDown
		}
		return resp("standby", false, 60), nil
	})
	r.e.RequestFresh()
	r.step() // one failure: still coordinated-or-starting, the request is kept
	eq(t, r.snap().FreshPending, true)
	fail = false
	r.step()
	reqs := r.syncer.reqs()
	eq(t, reqs[len(reqs)-1].WantFresh, true)
	r.step()
	reqs = r.syncer.reqs()
	eq(t, reqs[len(reqs)-1].WantFresh, false)
}

func TestRequestFreshWakesRun(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, r.e, r.clk); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, 2*time.Second, func() bool { return r.syncer.count() >= 1 })
	r.e.RequestFresh() // the fake clock is never advanced
	waitFor(t, 2*time.Second, func() bool { return r.syncer.count() >= 2 })
}

func TestFreshFollowUpsAreShort(t *testing.T) {
	cur := "2026-01-05T08:50:00.000Z"
	r := newRig(t, func(int, string, protocol.Request) (protocol.Response, error) {
		return withLatest(resp("standby", false, 60), cur), nil
	})
	eq(t, r.step(), 60*time.Second)
	eq(t, r.snap().FreshPending, false)
	r.e.RequestFresh()
	for i := 0; i < FreshFollowUps; i++ {
		eq(t, r.step(), MinNextSync)
		eq(t, r.snap().FreshPending, true)
	}
	eq(t, r.step(), 60*time.Second)
	eq(t, r.snap().FreshPending, false)

	// a new fetched_at ends the follow-ups at once
	r.e.RequestFresh()
	eq(t, r.step(), MinNextSync)
	cur = "2026-01-05T09:05:00.000Z"
	eq(t, r.step(), 60*time.Second)
	eq(t, r.snap().FreshPending, false)
}

func TestFreshInStandaloneDoesNotFetch(t *testing.T) {
	r := newRig(t, always(protocol.Response{}, errDown))
	for i := 0; i < FailuresToStandalone; i++ {
		r.step()
	}
	eq(t, r.snap().Mode, "standalone")
	calls, fetches := r.syncer.count(), r.fetcher.calls
	r.e.RequestFresh()
	r.e.Step(ctx) // the clock does not move: only the wake-up step
	if r.syncer.count() <= calls {
		t.Fatal("no sync attempt after RequestFresh")
	}
	eq(t, r.fetcher.calls, fetches)
	eq(t, r.snap().FreshPending, false)
}

func TestSetClientMode(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	d := r.e.d
	d.Identity.Mode = "tray"
	r.e = New(d)
	r.step()
	eq(t, string(r.syncer.reqs()[0].Client.Mode), "tray")
	eq(t, r.snap().ClientMode, "tray")
	r.e.SetClientMode("headless")
	r.step()
	eq(t, string(r.syncer.reqs()[1].Client.Mode), "headless")
	eq(t, r.snap().ClientMode, "headless")
}

func TestSnapshotSeverityAndScopedReset(t *testing.T) {
	body := `{"limits":[{"kind":"weekly_all","percent":40,"severity":"warning","resets_at":"2026-01-09T06:00:00+00:00"},` +
		`{"kind":"weekly_scoped","percent":76,"severity":"critical","resets_at":"2026-01-10T07:00:00+00:00","scope":{"model":{"display_name":"Fable"}}}]}`
	x := resp("standby", false, 60)
	x.Latest = &protocol.Latest{FetchedAt: "2026-01-05T08:59:00.000Z", Usage: json.RawMessage(body)}
	r := newRig(t, always(x, nil))
	r.step()
	s := r.snap()
	eq(t, s.WeeklySeverity, "warning")
	eq(t, s.ScopedSeverity, "critical")
	eq(t, s.ScopedResetsAt, time.Date(2026, 1, 10, 7, 0, 0, 0, time.UTC))
	eq(t, *s.ScopedPercent, 76.0)
}

func TestRequestFreshConcurrent(t *testing.T) {
	r := newRig(t, always(resp("standby", false, 60), nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, r.e, r.clk); close(done) }()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.e.RequestFresh()
			if i%2 == 0 {
				r.e.SetClientMode("tray")
			} else {
				r.e.SetClientMode("headless")
			}
			r.clk.Advance(time.Second)
		}(i)
	}
	wg.Wait()
	waitFor(t, 2*time.Second, func() bool { return r.syncer.count() >= 2 })
	cancel()
	<-done
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached within %v", limit)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRefreshCannotPassTheFetchLimit(t *testing.T) {
	// the server orders a fetch on every sync; the hourly budget runs out
	r := newRig(t, always(resp("poller", true, 15), nil))
	for r.fetcher.calls < anthropic.HourlyCap {
		r.step()
		if r.clk.Now().Sub(t0) > 2*time.Hour {
			t.Fatal("budget never used up")
		}
	}
	calls := r.fetcher.calls
	r.e.RequestFresh()
	r.e.Step(ctx) // at once, want_fresh, fetch_now: still no fetch
	eq(t, r.fetcher.calls, calls)
	reqs := r.syncer.reqs()
	eq(t, reqs[len(reqs)-1].WantFresh, true)
}
