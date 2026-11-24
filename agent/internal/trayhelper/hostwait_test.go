package trayhelper

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fakeHost is a session bus connection for waitForHost.
type fakeHost struct {
	has      atomic.Bool
	appeared chan struct{}
	closed   chan struct{}
	closeMu  sync.Once
}

func newFakeHost() *fakeHost {
	return &fakeHost{appeared: make(chan struct{}, 1), closed: make(chan struct{})}
}
func (f *fakeHost) Has(context.Context) (bool, error) { return f.has.Load(), nil }
func (f *fakeHost) Appeared() <-chan struct{}         { return f.appeared }
func (f *fakeHost) Closed() <-chan struct{}           { return f.closed }
func (f *fakeHost) Close()                            { f.closeMu.Do(func() { close(f.closed) }) }

type waitResult struct {
	ok      bool
	elapsed time.Duration
}

// startWait runs waitForHost with a fake clock in the background.
func startWait(ctx context.Context, connect func() (hostConn, error), limit time.Duration) (*clock.Fake, <-chan waitResult) {
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	done := make(chan waitResult, 1)
	go func() {
		ok := waitForHost(ctx, connect, clk, limit)
		done <- waitResult{ok, clk.Now().Sub(start)}
	}()
	return clk, done
}

func expect(t *testing.T, done <-chan waitResult) waitResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("waitForHost did not return")
		return waitResult{}
	}
}

func expectNothing(t *testing.T, done <-chan waitResult) {
	t.Helper()
	select {
	case r := <-done:
		t.Fatalf("returned early: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

// The tray host is noticed the moment its name appears on the bus, with no
// wait for the next poll (the fake clock never moves).
func TestHostNoticedBySignal(t *testing.T) {
	h := newFakeHost()
	_, done := startWait(context.Background(), func() (hostConn, error) { return h, nil }, 0)
	expectNothing(t, done)
	h.has.Store(true)
	h.appeared <- struct{}{}
	r := expect(t, done)
	if !r.ok || r.elapsed != 0 {
		t.Fatalf("%+v", r)
	}
	select {
	case <-h.closed:
	default:
		t.Fatal("connection not closed")
	}
}

func TestHostAlreadyThere(t *testing.T) {
	h := newFakeHost()
	h.has.Store(true)
	_, done := startWait(context.Background(), func() (hostConn, error) { return h, nil }, 0)
	if r := expect(t, done); !r.ok {
		t.Fatalf("%+v", r)
	}
}

// No session bus yet (GNOME is restarting it at logout): connecting is
// retried every hostRetry, and the host is found once the bus is back.
func TestHostWaitRetriesTheBus(t *testing.T) {
	var tries atomic.Int32
	h := newFakeHost()
	h.has.Store(true)
	clk, done := startWait(context.Background(), func() (hostConn, error) {
		if tries.Add(1) < 3 {
			return nil, errors.New("no bus")
		}
		return h, nil
	}, 0)
	for i := 0; i < 2; i++ {
		expectNothing(t, done)
		clk.Advance(hostRetry)
	}
	r := expect(t, done)
	if !r.ok || r.elapsed != 2*hostRetry || tries.Load() != 3 {
		t.Fatalf("%+v after %d tries", r, tries.Load())
	}
}

// The connection ends while waiting (the bus was restarted): a new
// connection is made and the host found on it.
func TestHostWaitReconnectsAfterTheBusCloses(t *testing.T) {
	first, second := newFakeHost(), newFakeHost()
	second.has.Store(true)
	var n atomic.Int32
	clk, done := startWait(context.Background(), func() (hostConn, error) {
		if n.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}, 0)
	expectNothing(t, done)
	first.Close()
	expectNothing(t, done)
	clk.Advance(hostRetry)
	if r := expect(t, done); !r.ok || n.Load() != 2 {
		t.Fatalf("%+v %d", r, n.Load())
	}
}

// A signal can be missed (or the host may not announce itself): the name
// is also checked every hostPoll.
func TestHostWaitPollsAsWell(t *testing.T) {
	h := newFakeHost()
	clk, done := startWait(context.Background(), func() (hostConn, error) { return h, nil }, 0)
	expectNothing(t, done)
	h.has.Store(true)
	expectNothing(t, done)
	clk.Advance(hostPoll)
	if r := expect(t, done); !r.ok || r.elapsed != hostPoll {
		t.Fatalf("%+v", r)
	}
}

func TestHostWaitGivesUpAtTheLimit(t *testing.T) {
	h := newFakeHost()
	clk, done := startWait(context.Background(), func() (hostConn, error) { return h, nil }, 3*time.Second)
	expectNothing(t, done)
	clk.Advance(3 * time.Second)
	if r := expect(t, done); r.ok {
		t.Fatalf("%+v", r)
	}
	select {
	case <-h.closed:
	default:
		t.Fatal("connection not closed")
	}
}

func TestHostWaitEndsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, done := startWait(ctx, func() (hostConn, error) { return nil, errors.New("no bus") }, 0)
	expectNothing(t, done)
	cancel()
	if r := expect(t, done); r.ok {
		t.Fatalf("%+v", r)
	}
}

func TestHostWaitFromEnv(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "AI_USAGE_TRAY_WAIT" {
				return v
			}
			return ""
		}
	}
	eq(t, WatcherWaitFrom(env("")), WatcherWait)
	eq(t, WatcherWaitFrom(env("3s")), 3*time.Second)
	eq(t, WatcherWaitFrom(env("nonsense")), WatcherWait)
	eq(t, WatcherWaitFrom(env("1ns")), minWatcherWait)
	eq(t, WatcherWaitFrom(env("-5s")), WatcherWait)
}

// waitForChange (a stopped helper's watch): a watcher taking its name, or
// the session bus coming back after it was lost or missing, is a change; a
// watcher already there is not.
func TestWaitForChange(t *testing.T) {
	run := func(ctx context.Context, connect func() (hostConn, error)) (*clock.Fake, <-chan bool) {
		clk := clock.NewFake(time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC))
		done := make(chan bool, 1)
		go func() { done <- waitForChange(ctx, connect, clk) }()
		return clk, done
	}
	get := func(done <-chan bool) bool {
		t.Helper()
		select {
		case ok := <-done:
			return ok
		case <-time.After(5 * time.Second):
			t.Fatal("waitForChange did not return")
			return false
		}
	}
	none := func(done <-chan bool) {
		t.Helper()
		select {
		case ok := <-done:
			t.Fatalf("returned %v", ok)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// a watcher already there is no change; one appearing is
	h := newFakeHost()
	h.has.Store(true)
	_, done := run(context.Background(), func() (hostConn, error) { return h, nil })
	none(done)
	h.appeared <- struct{}{}
	if !get(done) {
		t.Fatal("false")
	}

	// the bus went away and came back
	var n atomic.Int32
	first := newFakeHost()
	clk, done := run(context.Background(), func() (hostConn, error) {
		switch n.Add(1) {
		case 1:
			return first, nil
		case 2:
			return nil, errors.New("no bus")
		}
		return newFakeHost(), nil
	})
	none(done)
	first.Close()
	waitFor(t, "a second connect", func() bool { clk.Advance(hostRetry); return n.Load() >= 2 })
	waitFor(t, "a third connect", func() bool { clk.Advance(hostRetry); return n.Load() >= 3 })
	if !get(done) {
		t.Fatal("false")
	}

	// no bus at first, then one
	n.Store(0)
	clk, done = run(context.Background(), func() (hostConn, error) {
		if n.Add(1) == 1 {
			return nil, errors.New("no bus")
		}
		return newFakeHost(), nil
	})
	waitFor(t, "a second connect", func() bool { clk.Advance(hostRetry); return n.Load() >= 2 })
	if !get(done) {
		t.Fatal("false")
	}

	// the end of ctx ends it
	ctx, cancel := context.WithCancel(context.Background())
	_, done = run(ctx, func() (hostConn, error) { return newFakeHost(), nil })
	none(done)
	cancel()
	if get(done) {
		t.Fatal("true after cancel")
	}
}
