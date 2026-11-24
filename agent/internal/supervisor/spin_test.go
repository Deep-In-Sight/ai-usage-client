package supervisor

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// The review's crash (stage 2, I-1): a helper asked to quit is wanted
// again before it ended, and the start that follows its end fails. The
// failure must take the backoff, not start it again at once (which
// recursed until the stack overflowed).
func TestRewantedHelperWhoseStartFailsBacksOff(t *testing.T) {
	sim := newSim(t, func(n int) bool { return n >= 3 }, behaviour{name: "ignores-quit", ignoreQuit: true})
	sim.sup.Want([]provider.ID{provider.Claude}) // 1: claude
	sim.sup.Want(nil)                            // claude asked to quit (it does not end yet); 2: the neutral one
	sim.sup.Want([]provider.ID{provider.Claude}) // claude wanted again before it ended
	old := sim.procs(provider.Claude)[0]
	old.exit(0)                        // now it ends: 3 (fails)
	sim.waitSpawns(provider.Claude, 2) // the first, then the failed restart; not more
	if !sim.loggedf("cannot start") {
		t.Fatal("the failed start was not logged")
	}
	// the failure waits like any other: again after 1 s, then 2 s
	sim.clk.Advance(999 * time.Millisecond)
	sim.quiesce()
	eqInt(t, sim.spawnsOf(provider.Claude), 2)
	sim.clk.Advance(time.Millisecond)
	sim.waitSpawns(provider.Claude, 3)
	sim.clk.Advance(1999 * time.Millisecond)
	sim.quiesce()
	eqInt(t, sim.spawnsOf(provider.Claude), 3)
	sim.clk.Advance(time.Millisecond)
	sim.waitSpawns(provider.Claude, 4)
	sim.stop()
}

// waitSpawns waits until id was started n times, and checks that it is
// not started more often.
func (s *sim) waitSpawns(id provider.ID, n int) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.spawnsOf(id) < n {
		if time.Now().After(deadline) {
			s.t.Fatalf("%q started %d times, want %d", id, s.spawnsOf(id), n)
		}
		time.Sleep(time.Millisecond)
	}
	s.quiesce()
	eqInt(s.t, s.spawnsOf(id), n)
}

func eqInt(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

// No code path may start helpers in a tight loop: every combination of
// failing starts, helpers that end at once (with every exit code), that
// end after a while, that ignore quit, and changes of the wanted helpers
// is run on a fake clock, and the starts are counted per moment, per
// minute and in all.
func TestNoSpawnLoop(t *testing.T) {
	behaviours := []behaviour{
		{name: "lives"},
		{name: "ignores-quit", ignoreQuit: true},
		{name: "slow-quit", quitAfter: time.Second},
		{name: "exits-0", exitCode: 0, exitAfter: -1},
		{name: "exits-1", exitCode: 1, exitAfter: -1},
		{name: "exits-75", exitCode: trayproto.ExitSessionLost, exitAfter: -1},
		{name: "exits-76", exitCode: trayproto.ExitSessionEnded, exitAfter: -1},
		{name: "exits-77", exitCode: trayproto.ExitDesktopBack, exitAfter: -1}, // a watcher that always says the desktop is back
		{name: "killed", exitCode: -1, exitAfter: -1},
		{name: "75-after-10s", exitCode: trayproto.ExitSessionLost, exitAfter: quickLoss},
		{name: "1-after-10min", exitCode: 1, exitAfter: healthyAfter},
	}
	spawns := map[string]func(n int) bool{
		"ok":           func(int) bool { return false },
		"fails":        func(int) bool { return true },
		"fails-from-3": func(n int) bool { return n >= 3 },
		"every-other":  func(n int) bool { return n%2 == 0 },
	}
	A, B := provider.Claude, provider.Codex
	scripts := map[string][]want{
		"steady":       {{0, []provider.ID{A}, ""}},
		"requit":       {{0, []provider.ID{A}, ""}, {0, nil, ""}, {0, []provider.ID{A}, ""}},
		"requit-later": {{0, []provider.ID{A}, ""}, {time.Second, nil, ""}, {time.Second, []provider.ID{A}, ""}, {3 * time.Second, nil, ""}, {4 * time.Second, []provider.ID{A}, ""}},
		"two":          {{0, []provider.ID{A, B}, ""}, {30 * time.Second, []provider.ID{A}, ""}, {30 * time.Second, []provider.ID{A, B}, ""}, {2 * time.Minute, nil, ""}, {2 * time.Minute, []provider.ID{B, A}, ""}},
		"toggle":       toggles(7*time.Second, 10*time.Minute, []provider.ID{A}, nil),
		"views":        views(10*time.Second, 20*time.Minute, []provider.ID{A}),
	}
	const simFor = 40 * time.Minute
	for _, b := range behaviours {
		for sn, fail := range spawns {
			for cn, script := range scripts {
				t.Run(fmt.Sprintf("%s/%s/%s", b.name, sn, cn), func(t *testing.T) {
					t.Parallel()
					sim := newSim(t, fail, b)
					sim.play(script, simFor)
					sim.stop()
					sim.check(script, simFor, sn == "fails" || b.alwaysFails())
				})
			}
		}
	}
}

// toggles wants a, then b, every gap for d.
func toggles(gap, d time.Duration, a, b []provider.ID) []want {
	var out []want
	for at, i := time.Duration(0), 0; at <= d; at, i = at+gap, i+1 {
		ids := a
		if i%2 == 1 {
			ids = b
		}
		out = append(out, want{at, ids, ""})
	}
	return out
}

// views wants ids, then shows a new view every gap for d (a stopped
// helper is tried again when the view changes).
func views(gap, d time.Duration, ids []provider.ID) []want {
	out := []want{{0, ids, ""}}
	for at, i := gap, 0; at <= d; at, i = at+gap, i+1 {
		out = append(out, want{at, nil, fmt.Sprintf("%d%%", i)})
	}
	return out
}

// want is a change of the wanted helpers, or (view set) a new view.
type want struct {
	at   time.Duration
	ids  []provider.ID
	view string
}

// behaviour is what a started helper does.
type behaviour struct {
	name       string
	ignoreQuit bool
	exitCode   int
	// exitAfter: 0 lives until quit; negative ends at once; else ends
	// after that long.
	exitAfter time.Duration
	// quitAfter: ends this long after it was asked to quit (0: at once)
	quitAfter time.Duration
}

// alwaysFails: every start ends as a failure (a watcher's 77 too, for a
// helper).
func (b behaviour) alwaysFails() bool {
	return b.exitAfter < 0 && b.exitCode != trayproto.ExitSessionEnded
}

type spawnRec struct {
	id      provider.ID
	at      time.Time
	watcher bool // a helper in standby (Options.Standby)
}

// procState is what the simulation knows of one fake process, so that it
// moves the clock only once everything due at this moment has happened.
type procState struct {
	exitDue   time.Time // it ends by itself then (zero: not by itself)
	quitAt    time.Time // it got quit then (zero: not yet)
	quitAfter time.Duration
	ignores   bool // it does not end on quit (the supervisor kills it after stopTimeout)
	exited    bool
}

// sim is a supervisor on a fake clock whose helpers behave as told.
type sim struct {
	t      *testing.T
	clk    *clock.Fake
	t0     time.Time
	sup    *Supervisor
	logBuf *syncBuffer

	mu       sync.Mutex
	recs     []spawnRec
	runaway  []string
	started  map[provider.ID][]*fakeProc
	states   map[*fakeProc]*procState
	ended    int // processes that ended
	failed   int // helper starts that failed (each is an exit event; a watcher's is not)
	exits    int // exit events delivered
	activity atomic.Int64
	stopped  chan struct{}
}

const runawayAt = 50 // starts of one helper at one moment that end the loop

func newSim(t *testing.T, fail func(n int) bool, b behaviour) *sim {
	s := &sim{t: t, t0: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC), logBuf: &syncBuffer{}, started: map[provider.ID][]*fakeProc{},
		states: map[*fakeProc]*procState{}, stopped: make(chan struct{})}
	s.clk = clock.NewFake(s.t0)
	n := 0
	s.sup = New(Options{
		Spawn: func(p provider.ID, uid uint32, token string) (Proc, error) {
			s.activity.Add(1)
			s.mu.Lock()
			n++
			now := s.clk.Now()
			// Spawn runs under the supervisor's lock: its slot can be read
			sl := s.sup.slots[p]
			watcher := sl != nil && sl.standby
			s.recs = append(s.recs, spawnRec{p, now, watcher})
			same := 0
			for _, r := range s.recs {
				if r.id == p && r.at.Equal(now) {
					same++
				}
			}
			runaway := same > runawayAt
			if runaway && len(s.runaway) == 0 {
				s.runaway = append(s.runaway, fmt.Sprintf("%q started %d times at %s", p, same, now.Sub(s.t0)))
			}
			failNow := fail(n) && !runaway // a runaway loop is ended by a start that works
			if failNow && !watcher {
				s.failed++
			}
			s.mu.Unlock()
			if failNow {
				return nil, errors.New("test: cannot start")
			}
			st := &procState{quitAfter: b.quitAfter, ignores: b.ignoreQuit || b.quitAfter > 0}
			var fp *fakeProc
			fp = newHookedProc(p, uid, token, st.ignores, func(m trayproto.Msg) {
				if m.Type != trayproto.TypeQuit {
					return
				}
				s.mu.Lock()
				st.quitAt = s.clk.Now()
				s.mu.Unlock()
				if b.quitAfter > 0 {
					ch := s.clk.After(b.quitAfter)
					go func() {
						select {
						case <-ch:
							fp.exit(0)
						case <-s.stopped:
						}
					}()
				}
			}, func() {
				s.mu.Lock()
				st.exited = true
				s.ended++
				s.mu.Unlock()
			})
			s.mu.Lock()
			s.started[p] = append(s.started[p], fp)
			s.states[fp] = st
			if b.exitAfter > 0 && !runaway {
				st.exitDue = now.Add(b.exitAfter)
			}
			s.mu.Unlock()
			switch {
			case runaway:
			case b.exitAfter < 0:
				fp.exit(b.exitCode) // at once: Wait returns as soon as the supervisor waits
			case b.exitAfter > 0:
				ch := s.clk.After(b.exitAfter)
				go func() {
					select {
					case <-ch:
						fp.exit(b.exitCode)
					case <-s.stopped:
					}
				}()
			}
			return fp, nil
		},
		Standby: true,
		Clock:   s.clk,
		Log:     log.New(s.logBuf, "", 0),
	})
	go func() {
		for {
			select {
			case ev := <-s.sup.Events():
				if ev.Exit != nil {
					s.mu.Lock()
					s.exits++
					s.mu.Unlock()
				}
				s.activity.Add(1)
			case <-s.stopped:
				return
			}
		}
	}()
	t.Cleanup(func() { close(s.stopped) })
	return s
}

// settled: everything due at this moment of the fake clock has happened.
// Every process that ended had its end handled (review n-1: an exit seen
// a timer step late shifted the policy's moments), none that is to end
// by now still runs, and no quit is on its way.
func (s *sim) settled() bool {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exits != s.ended+s.failed {
		return false
	}
	now := s.clk.Now()
	for _, st := range s.states {
		if st.exited {
			continue
		}
		due := func(t time.Time) bool { return !t.IsZero() && !now.Before(t) }
		switch {
		case due(st.exitDue):
			return false
		case st.quitAt.IsZero():
		case !st.ignores, st.quitAfter > 0 && due(st.quitAt.Add(st.quitAfter)), due(st.quitAt.Add(stopTimeout)):
			// ends on quit, or ends or is killed by now
			return false
		}
	}
	for _, sl := range s.sup.slots {
		if fp, ok := sl.proc.(*fakeProc); ok && sl.quitSent {
			if st := s.states[fp]; st != nil && !st.exited && st.quitAt.IsZero() {
				return false // the quit is on its way
			}
		}
	}
	return true
}

// quiesce waits until everything due now has happened and nothing else
// has for a few milliseconds.
func (s *sim) quiesce() {
	deadline := time.Now().Add(20 * time.Second)
	for {
		for !s.settled() {
			if time.Now().After(deadline) {
				s.t.Fatal("the simulation does not settle")
			}
			time.Sleep(100 * time.Microsecond)
		}
		last := s.activity.Load()
		for quiet := 0; quiet < 3; {
			time.Sleep(time.Millisecond)
			if n := s.activity.Load(); n != last {
				last, quiet = n, 0
				continue
			}
			quiet++
		}
		if s.settled() {
			return
		}
	}
}

func (s *sim) procs(id provider.ID) []*fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.started[id])
}

func (s *sim) spawnsOf(id provider.ID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.recs {
		if r.id == id {
			n++
		}
	}
	return n
}

func (s *sim) loggedf(sub string) bool { return strings.Contains(s.logBuf.String(), sub) }

// play runs the script and then lets the time run on from one timer to
// the next until simFor has passed.
func (s *sim) play(script []want, simFor time.Duration) {
	end := s.t0.Add(simFor)
	todo := slices.Clone(script)
	for steps := 0; ; steps++ {
		if steps > 200000 {
			s.t.Fatal("the simulation does not end")
		}
		s.quiesce()
		now := s.clk.Now()
		for len(todo) > 0 && !s.t0.Add(todo[0].at).After(now) {
			if v := todo[0].view; v != "" {
				s.sup.Show(view(v))
			} else {
				s.sup.Want(todo[0].ids)
			}
			todo = todo[1:]
			s.quiesce()
		}
		if !now.Before(end) {
			return
		}
		next := end
		if n, ok := s.clk.Next(); ok && n.Before(next) {
			next = n
		}
		if len(todo) > 0 && s.t0.Add(todo[0].at).Before(next) {
			next = s.t0.Add(todo[0].at)
		}
		if !next.After(now) {
			next = now.Add(time.Millisecond)
		}
		s.clk.Set(next)
	}
}

// stop stops the supervisor, letting the fake time run for its kill
// timeout.
func (s *sim) stop() {
	done := make(chan struct{})
	go func() { s.sup.Stop(); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-done:
			return
		case <-time.After(5 * time.Millisecond):
			if time.Now().After(deadline) {
				s.t.Fatal("Stop did not return")
			}
			s.clk.Advance(time.Second)
		}
	}
}

// check counts the starts: per helper and moment at most one more than
// the changes of the wanted helpers then; per minute at most a series of
// failures per change of the wanted helpers, and the restarts after lost
// sessions; in all, when every start fails, a series per stop.
func (s *sim) check(script []want, simFor time.Duration, alwaysFails bool) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runaway) > 0 {
		s.t.Fatalf("a start loop: %v", s.runaway)
	}
	wantsIn := func(from, to time.Duration) int {
		n := 0
		for _, w := range script {
			if w.at >= from && w.at <= to && w.view == "" { // a new view may not start more
				n++
			}
		}
		return n
	}
	ids := map[provider.ID][]time.Duration{}
	type moment struct {
		id      provider.ID
		at      time.Duration
		watcher bool
	}
	perMoment := map[moment]int{}
	for _, r := range s.recs {
		ids[r.id] = append(ids[r.id], r.at.Sub(s.t0))
		perMoment[moment{r.id, r.at.Sub(s.t0), r.watcher}]++
	}
	for m, n := range perMoment {
		limit := 1 + wantsIn(m.at, m.at) // a helper
		if m.watcher {
			limit = 1 // a watcher, when the helper stops
		}
		if n > limit {
			s.t.Fatalf("%q started %d times at %s (watcher %v, limit %d)", m.id, n, m.at, m.watcher, limit)
		}
	}
	for id, at := range ids {
		for i := range at {
			inMinute := 0
			for j := i; j < len(at) && at[j] < at[i]+time.Minute; j++ {
				inMinute++
			}
			// each change of the wanted helpers may start a series of
			// failures anew; helpers that ran 10 s before their session
			// went away come back after 1 s; a stopped one is tried once
			// a minute (view changes, the desktop back) beside its watcher
			limit := (wantsIn(at[i], at[i]+time.Minute)+1)*(failLimit+1) + int(time.Minute/(quickLoss+lostDelay)) + 2
			if inMinute > limit {
				s.t.Fatalf("%q started %d times in the minute from %s (limit %d)", id, inMinute, at[i], limit)
			}
		}
		if alwaysFails {
			// a series per stop, and once stopped a retry and a watcher a
			// minute at most
			limit := (int(simFor/stoppedFor)+1)*(failLimit+1) + 2*(int(simFor/probeGap)+1) + 2*wantsIn(0, simFor)
			if len(at) > limit {
				s.t.Fatalf("%q started %d times in %s although every start fails (limit %d)", id, len(at), simFor, limit)
			}
		}
	}
}
