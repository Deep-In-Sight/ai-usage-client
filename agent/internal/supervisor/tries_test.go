package supervisor

import (
	"reflect"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// A helper that keeps failing is tried one at a time once it is stopped,
// and each failed try doubles the wait before the next, up to 5 minutes
// (review n-2: the view changes every minute, so it was tried once a
// minute for ever).
func TestFailedTriesDoubleTheGap(t *testing.T) {
	sim := newSim(t, func(int) bool { return false }, behaviour{name: "exits-1", exitCode: 1, exitAfter: -1})
	script := views(10*time.Second, 40*time.Minute, []provider.ID{provider.Claude})
	sim.play(script, 40*time.Minute)
	sim.stop()
	sim.mu.Lock()
	defer sim.mu.Unlock()
	var helpers, watchers []time.Duration
	for _, r := range sim.recs {
		if r.watcher {
			watchers = append(watchers, r.at.Sub(sim.t0))
		} else {
			helpers = append(helpers, r.at.Sub(sim.t0))
		}
	}
	// the series of five (1, 2, 4, 8 s apart), then the tries
	series := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second}
	if len(helpers) < len(series)+6 || !reflect.DeepEqual(helpers[:len(series)], series) {
		t.Fatalf("starts %v", helpers)
	}
	tries := helpers[len(series)-1:]
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i := 1; i < len(tries); i++ {
		gap := tries[i] - tries[i-1]
		exp := stoppedFor
		if i-1 < len(want) {
			exp = want[i-1]
		}
		if gap != exp {
			t.Fatalf("try %d came %v after the one before, want %v (starts %v)", i, gap, exp, tries)
		}
	}
	// one watcher after the stop and after each failed try, no more
	if len(watchers) != len(tries) {
		t.Fatalf("%d watchers for %d stops: %v", len(watchers), len(tries), watchers)
	}
}

// The legitimate path behind review n-1: a helper whose fifth failure came
// a minute after its last start is stopped and watched; the watcher says
// the desktop is back, the try that follows at once fails at once, and a
// second watcher starts at that same moment. Then nothing until the
// doubled gap has passed.
func TestWatcherAgainAfterAFailedTry(t *testing.T) {
	h := newHarnessWith(t, true)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		p.exit(3)
		h.event(isExit)
		h.clk.Advance(wait)
		p = h.spawned()
	}
	h.clk.Advance(time.Minute) // the fifth lived a minute
	p.exit(3)
	h.event(func(e Event) bool { return isExit(e) && e.RestartIn == stoppedFor })
	w1, hello := h.spawnedHello()
	if !hello.Standby {
		t.Fatalf("%+v", hello)
	}
	w1.exit(trayproto.ExitDesktopBack) // its last start was a minute ago: tried at once
	try, hello := h.spawnedHello()
	if hello.Standby {
		t.Fatalf("%+v", hello)
	}
	try.exit(3)
	w2, hello := h.spawnedHello()
	if !hello.Standby {
		t.Fatalf("%+v", hello)
	}
	w2.exit(trayproto.ExitDesktopBack) // the try was just now: two minutes after it
	h.event(func(e Event) bool { return isExit(e) && e.Provider == provider.Claude })
	h.clk.Advance(2*time.Minute - time.Millisecond)
	h.noSpawn()
	h.clk.Advance(time.Millisecond)
	if _, hello := h.spawnedHello(); hello.Standby {
		t.Fatalf("%+v", hello)
	}
}

// A Windows helper that ends without removing its icon (a crash, a kill)
// leaves it in the notification area until the pointer passes over it.
// The next helper started is told the icon's window and id and removes it
// (review n-2: no ghost icon per try); one that quit cleanly leaves none.
func TestStaleIconGoesToTheNextHelper(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p, hello := h.spawnedHello()
	if len(hello.Stale) != 0 {
		t.Fatalf("%+v", hello.Stale)
	}
	p.say(t, trayproto.Msg{Type: trayproto.TypeReady, Window: 4242})
	h.event(func(e Event) bool { return e.Ready })
	p.exit(3)
	h.event(isExit)
	h.clk.Advance(time.Second)
	p, hello = h.spawnedHello()
	eqStale(t, hello.Stale, []trayproto.StaleIcon{{Window: 4242, UID: 100}})

	// told once: the helper after this one is not told again
	p.say(t, trayproto.Msg{Type: trayproto.TypeReady, Window: 4343})
	h.event(func(e Event) bool { return e.Ready })
	h.sup.Want([]provider.ID{provider.Codex})
	c, hello := h.spawnedHello()
	if c.id != provider.Codex || len(hello.Stale) != 0 {
		t.Fatalf("%q %+v", c.id, hello.Stale)
	}
	// claude quits cleanly (0 after quit): it removed its icon itself
	h.event(func(e Event) bool { return isExit(e) && e.Provider == provider.Claude })
	c.exit(3)
	h.event(isExit)
	h.clk.Advance(time.Second)
	if _, hello := h.spawnedHello(); len(hello.Stale) != 0 {
		t.Fatalf("%+v", hello.Stale)
	}
}

// A helper killed after it ignored quit leaves its icon: the next helper
// of any provider removes it; so does the watcher of a stopped helper.
func TestStaleIconOfAKilledOrStoppedHelper(t *testing.T) {
	h := newHarnessWith(t, true)
	h.ignoreQuit = true
	h.sup.Want([]provider.ID{provider.Claude})
	a, _ := h.spawnedHello()
	a.say(t, trayproto.Msg{Type: trayproto.TypeReady, Window: 11})
	h.event(func(e Event) bool { return e.Ready })
	h.sup.Want([]provider.ID{provider.Codex}) // claude ignores quit
	c, _ := h.spawnedHello()
	h.clk.Advance(stopTimeout)
	h.event(func(e Event) bool { return isExit(e) && e.Provider == provider.Claude })
	// codex fails five times; its fifth showed its icon first
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		c.exit(3)
		h.event(isExit)
		h.clk.Advance(wait)
		var hello trayproto.Msg
		c, hello = h.spawnedHello()
		if want := i == 0; want != (len(hello.Stale) == 1) {
			t.Fatalf("start %d: stale %+v", i+2, hello.Stale)
		}
		if i == 0 {
			eqStale(t, hello.Stale, []trayproto.StaleIcon{{Window: 11, UID: 100}})
		}
	}
	c.say(t, trayproto.Msg{Type: trayproto.TypeReady, Window: 22})
	h.event(func(e Event) bool { return e.Ready })
	c.exit(3)
	w, hello := h.spawnedHello()
	if !hello.Standby {
		t.Fatalf("%+v", hello)
	}
	eqStale(t, hello.Stale, []trayproto.StaleIcon{{Window: 22, UID: 101}})
	_ = w
}

// spawnedHello is the next process started and its first line.
func (h *harness) spawnedHello() (*fakeProc, trayproto.Msg) {
	h.t.Helper()
	select {
	case p := <-h.spawns:
		m := p.next(h.t)
		if m.Type != trayproto.TypeHello || m.Token != p.token {
			h.t.Fatalf("first line %+v", m)
		}
		return p, m
	case <-time.After(5 * time.Second):
		h.t.Fatal("no helper started")
		return nil, trayproto.Msg{}
	}
}

func eqStale(t *testing.T, got, want []trayproto.StaleIcon) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale icons %+v, want %+v", got, want)
	}
}
