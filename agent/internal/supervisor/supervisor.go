// Package supervisor runs the tray helpers: one `ai-usage tray-helper`
// process per provider in use (design D3, 6.4), each showing one tray
// icon, and on GNOME the bridge, a helper in the shell role that serves
// the usage panel to the GNOME Shell extension (drawn-panel design 6.4).
// The main process keeps collecting and syncing whatever happens to them;
// the supervisor starts a helper again when it ends (sooner for a lost
// session, later for repeated failures) and stops them all when the agent
// stops. While the panel extension is attached it runs no icon helper
// (SetPanelAttached). It talks to them only through their standard input
// and output (package trayproto).
package supervisor

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// Proc is one running helper.
type Proc interface {
	Stdin() io.WriteCloser
	// Stdout is closed by the supervisor once it stops reading, when it
	// is an io.Closer.
	Stdout() io.Reader
	// Wait returns the exit code once the process ended (-1 when it was
	// killed by a signal or the code is unknown).
	Wait() (int, error)
	Kill()
}

// Options are the supervisor's inputs.
type Options struct {
	// Spawn starts the helper for p (provider "" is the neutral one) with
	// its Windows icon id and the one-time token (real: ExecSpawner).
	Spawn func(p provider.ID, uid uint32, token string) (Proc, error)
	// IconDir is the folder for p's Windows icon files, sent in its first
	// line (nil: none).
	IconDir func(p provider.ID) string
	// Standby: while a helper is stopped after repeated failures, a helper
	// in standby (no icon) watches the desktop and says when it came back
	// (Linux: a tray host appeared or the session bus is back; Windows:
	// TaskbarCreated). Off where the helper cannot watch (macOS).
	Standby bool
	// BridgeEnableOnce is asked at every start of the bridge whether its
	// hello may ask it to turn the panel extension on once (the agent
	// outlives a logout, and the user's own "off" in the Extensions app
	// must hold: pre-flight ruling F15); nil: yes.
	BridgeEnableOnce func() bool
	Clock            clock.Clock
	Log              *log.Logger
}

// BridgeSlot is the bridge's slot (plan PR-9): apart from the providers'
// slots and the neutral one, and never touched by Want.
const BridgeSlot provider.ID = "#shell"

// Event is what happened to a helper.
type Event struct {
	Provider provider.ID
	Click    string // an item was clicked: its id
	Ready    bool   // the helper's icon is up
	Waiting  string // the helper's icon is not up, and why
	Exit     *int   // the helper ended with this code
	// RestartIn (with Exit) is when it is started again; negative: not.
	RestartIn time.Duration
	// Stopped (with Exit): the helper is stopped after repeated failures;
	// RestartIn is its stop.
	Stopped bool
	// The bridge's shell line (Provider BridgeSlot): the panel extension
	// attached (true) or went away (false); a GNOME Shell the panel
	// supports answered (Supported true) or the shell is not one (false);
	// turning the extension on succeeded (Enabled). Why says why attached
	// or supported is false ("detach", "owner", "ping", "unsupported 44.2",
	// "no shell": the bridge then waits on for a shell).
	Attached  *bool
	Supported *bool
	Enabled   bool
	Why       string
}

// IconView is one helper's own part of the view.
type IconView struct {
	Icon           trayproto.Icon
	Title, Tooltip string
}

// Restart timing (design 6.4).
const (
	lostDelay     = time.Second      // after a lost session (exit 75)
	endedDelay    = 30 * time.Second // after "the session is ending" (76): it did not end after all
	failWindow    = 10 * time.Minute // failures counted over this long
	failLimit     = 5                // this many in failWindow stop the helper …
	stoppedFor    = 5 * time.Minute  // … for this long (or until Want changes, the view changes or the desktop came back)
	probeGap      = time.Minute      // a stopped helper is tried at most this often besides its stop timer, doubled after each failed try (up to stoppedFor)
	healthyAfter  = 10 * time.Minute // a helper that lived this long starts the backoff again
	quickLoss     = 10 * time.Second // a session loss sooner than this after start counts as a failure
	stopTimeout   = 3 * time.Second  // Stop waits this long for helpers to quit, then kills them
	maxBackoffIdx = 6
	maxStale      = 8 // icons left behind that the next helper removes
)

// backoff is the wait before the n-th failure restart in a row.
var backoff = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second}

// UIDFor is the Windows notification icon id of p's helper: Claude and the
// neutral helper keep v0.8's 100 (settings the user made for the icon stay
// with it), Codex has 101 (design D3).
func UIDFor(p provider.ID) uint32 {
	if p == provider.Codex {
		return 101
	}
	return 100
}

// Supervisor runs the helpers. Its methods may be called from any
// goroutine.
type Supervisor struct {
	o     Options
	mu    sync.Mutex
	order []provider.ID
	slots map[provider.ID]*slot
	items []trayproto.Item
	icons map[provider.ID]IconView
	panel *trayproto.Panel
	// the once-only notice, until a helper that can show it took it
	// (sendNotice)
	notice string
	stop   bool
	done   chan struct{} // closed by Stop: pending restarts end
	queue  *eventQueue
	// stale: Windows icons of helpers that ended without removing them
	// (a crash, a kill); the next helper started removes them
	stale []trayproto.StaleIcon
	// attached: the usage panel is attached (or awaited): no icon helper
	// runs (SetPanelAttached)
	attached bool
	// the bridge's enable-once: allowed by WantBridge, and spent once a
	// bridge said it turned the extension on (never asked again by this
	// process)
	bridgeOnce, bridgeEnabled bool
}

type slot struct {
	id          provider.ID
	wanted      bool
	gen         int // counts starts; a stale exit or timer is ignored
	proc        Proc
	out         *sender
	ready       bool // an icon helper's icon is up; the bridge said supported:true or attached:true
	started     time.Time
	quitSent    bool
	exited      chan struct{} // closed when the current process ended
	sentItems   []trayproto.Item
	sentIcon    IconView
	sentPanel   *trayproto.Panel
	sentView    bool
	consecutive int         // failure exits in a row
	failures    []time.Time // failure exits within failWindow
	stopped     bool        // after failLimit failures, until a retry
	standby     bool        // the process is a watcher (stopped), not an icon
	lastStart   time.Time   // the last start of an icon helper (or its attempt)
	plan        int         // counts planned starts; a start voids older ones
	probeAt     time.Time   // a retry of the stopped helper is planned then
	trying      bool        // the process is a try of the stopped helper
	tries       int         // tries of the stopped helper that failed (each doubles the gap to the next)
	window      uint64      // Windows: the window of the process's icon (from its ready)
	// parked: the bridge found a GNOME Shell of a version the panel does
	// not support (exit 78; a bus without a shell yet it waits on itself);
	// it starts again only on a new session bus (an icon helper's exit 75)
	// or WantBridge off and on (plan PR-10)
	parked bool
}

// tryGap is how long after its last start a stopped helper may be tried
// again (review n-2): a minute, doubled after each failed try, at most
// stoppedFor.
func tryGap(tries int) time.Duration {
	g := probeGap
	for i := 0; i < tries && g < stoppedFor; i++ {
		g *= 2
	}
	return min(g, stoppedFor)
}

// New makes a supervisor; it starts nothing until Want.
func New(o Options) *Supervisor {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	return &Supervisor{o: o, slots: map[provider.ID]*slot{}, done: make(chan struct{}), queue: newEventQueue()}
}

// Events delivers what happens to the helpers, in order.
func (s *Supervisor) Events() <-chan Event { return s.queue.out }

// Want sets the helpers to run: one per provider, in this order; none
// means one neutral helper (provider ""). Helpers no longer wanted are
// asked to quit (and killed when they have not ended 3 s later); new ones
// start; a helper stopped after repeated failures is tried again.
func (s *Supervisor) Want(ids []provider.ID) {
	if len(ids) == 0 {
		ids = []provider.ID{""}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop || slices.Equal(ids, s.order) {
		return
	}
	s.order = slices.Clone(ids)
	for id, sl := range s.slots {
		if id != BridgeSlot && !slices.Contains(ids, id) {
			sl.wanted = false
			if sl.proc != nil {
				s.quitOrKill(sl)
			} else {
				delete(s.slots, id)
			}
		}
	}
	for _, id := range ids {
		sl := s.slots[id]
		switch {
		case sl == nil:
			sl = &slot{id: id, wanted: true}
			s.slots[id] = sl
			s.start(sl)
		case !sl.wanted:
			// asked to quit and wanted again before it ended: start anew
			// once it has
			sl.wanted = true
		case sl.stopped:
			sl.stopped, sl.failures, sl.consecutive, sl.tries = false, nil, 0, 0
			if sl.proc != nil {
				s.quitOrKill(sl) // its watcher: the helper starts once it ended
			} else {
				s.start(sl) // a new start voids the pending retry
			}
		}
	}
}

// WantBridge starts (on) or ends (off) the bridge, in its own slot apart
// from Want's (plan PR-9). Its hello carries the role "shell" and, when
// enableOnce allows it and Options.BridgeEnableOnce says so at that start,
// enable_once. A bridge that found an unsupported shell (exit 78) is started
// again by WantBridge off and on, or by an icon helper's exit 75 (a new
// session bus; plan PR-10); its other ends follow the helpers' restart
// rules (it never gets a standby watcher).
func (s *Supervisor) WantBridge(on, enableOnce bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop {
		return
	}
	s.bridgeOnce = enableOnce
	sl := s.slots[BridgeSlot]
	switch {
	case !on && sl == nil:
	case !on:
		sl.wanted = false
		if sl.proc != nil {
			s.quitOrKill(sl)
		} else {
			delete(s.slots, BridgeSlot)
		}
	case sl == nil:
		sl = &slot{id: BridgeSlot, wanted: true}
		s.slots[BridgeSlot] = sl
		s.start(sl)
	case !sl.wanted:
		// asked to quit and wanted again before it ended: it starts anew
		// once it has
		sl.wanted = true
	}
}

// SetPanelAttached holds the icon helpers back while the usage panel is
// attached, or awaited (design 6.4): on, every icon helper (the neutral
// one too) is asked to quit (and killed 3 s later if it has not ended) and
// none starts, whatever Want says or a restart plans; off, Want's helpers
// start again at once (a stopped one too). The bridge is not touched.
func (s *Supervisor) SetPanelAttached(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop || s.attached == on {
		return
	}
	s.attached = on
	if on {
		for id, sl := range s.slots {
			if id == BridgeSlot {
				continue
			}
			sl.plan++ // pending restarts and tries are void
			sl.probeAt = time.Time{}
			if sl.proc != nil {
				s.quitOrKill(sl)
			}
		}
	} else {
		for _, id := range s.order {
			sl := s.slots[id]
			if sl == nil || !sl.wanted {
				continue
			}
			sl.stopped, sl.failures, sl.consecutive, sl.tries, sl.trying = false, nil, 0, 0, false
			if sl.proc == nil {
				s.start(sl) // one still quitting starts once it ended
			}
		}
	}
	s.sendNotice()
}

// Show sets the view: the same menu and usage panel for every helper
// (design 6.5: a Linux icon helper ignores the panel), and each helper's
// own icon, title and tooltip. Only what changed is sent. A change tries a
// stopped helper again (at most once a minute): the user has no icon to
// click. panel (nil: none) is copied: the caller may change its own
// afterwards.
func (s *Supervisor) Show(items []trayproto.Item, icons map[provider.ID]IconView, panel *trayproto.Panel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := !reflect.DeepEqual(items, s.items) || !reflect.DeepEqual(icons, s.icons) || !reflect.DeepEqual(panel, s.panel)
	s.items = slices.Clone(items)
	s.icons = icons
	s.panel = clonePanel(panel)
	for _, sl := range s.slots {
		s.sendView(sl)
		if changed {
			s.probe(sl, "what it shows changed")
		}
	}
}

// clonePanel is a deep copy of p (nil stays nil, an empty list stays
// empty): nothing in it is shared with p.
func clonePanel(p *trayproto.Panel) *trayproto.Panel {
	if p == nil {
		return nil
	}
	c := *p
	c.Sections = slices.Clone(p.Sections)
	for i := range c.Sections {
		c.Sections[i].Rows = slices.Clone(c.Sections[i].Rows)
		for j := range c.Sections[i].Rows {
			c.Sections[i].Rows[j].Percent = cloneFloat(c.Sections[i].Rows[j].Percent)
		}
	}
	c.Actions = slices.Clone(p.Actions)
	c.Top = slices.Clone(p.Top)
	for i := range c.Top {
		c.Top[i].Percent = cloneFloat(c.Top[i].Percent)
	}
	return &c
}

func cloneFloat(f *float64) *float64 {
	if f == nil {
		return nil
	}
	v := *f
	return &v
}

// Notice is shown once: by the first helper whose icon is up, or by the
// bridge while the panel is attached (sendNotice).
func (s *Supervisor) Notice(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notice = text
	s.sendNotice()
}

// AnyReady reports whether some helper's icon is up.
func (s *Supervisor) AnyReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sl := range s.slots {
		if id != BridgeSlot && sl.ready {
			return true
		}
	}
	return false
}

// Stop asks every helper to quit, waits up to 3 s, then kills those left,
// and waits for them to end. Nothing starts after Stop.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.stop {
		s.stop = true
		close(s.done)
	}
	var waits []chan struct{}
	var procs []Proc
	for _, sl := range s.slots {
		sl.wanted = false
		if sl.proc != nil {
			s.quit(sl)
			waits = append(waits, sl.exited)
			procs = append(procs, sl.proc)
		}
	}
	s.mu.Unlock()
	timeout := s.o.Clock.After(stopTimeout)
	for i, w := range waits {
		select {
		case <-w:
			continue
		case <-timeout:
		}
		for j := i; j < len(waits); j++ {
			select {
			case <-waits[j]:
			default:
				procs[j].Kill()
			}
		}
		break
	}
	for _, w := range waits {
		<-w
	}
}

// start starts sl's helper (s.mu held). It never starts one again by
// itself: a failed start is a failure exit (retry), whose new start waits
// for a timer.
func (s *Supervisor) start(sl *slot) {
	if s.stop || !sl.wanted || sl.parked || s.held(sl) {
		return
	}
	now := s.o.Clock.Now()
	sl.plan++ // pending retries are void
	// a start while stopped is a try: if it fails, the helper is stopped
	// again at once, with a longer gap before the next try
	sl.trying = sl.stopped
	sl.stopped, sl.probeAt, sl.lastStart = false, time.Time{}, now
	p, token, ok := s.spawn(sl, false, now)
	if !ok {
		code := -1
		s.retry(sl, Event{Provider: sl.id, Exit: &code, RestartIn: -1}, code, 0, now)
		return
	}
	s.launch(sl, p, token)
	s.sendView(sl)
}

// startWatcher starts sl's helper in standby, to watch the desktop while
// the helper is stopped (s.mu held). A failed start is logged only: the
// stop timer stays.
func (s *Supervisor) startWatcher(sl *slot) {
	if s.stop || !sl.wanted || !s.o.Standby || sl.proc != nil || sl.id == BridgeSlot || s.held(sl) {
		return
	}
	if p, token, ok := s.spawn(sl, true, s.o.Clock.Now()); ok {
		s.o.Log.Printf("tray helper %s: watching the desktop for its tray to come back", name(sl.id))
		s.launch(sl, p, token)
	}
}

// held: sl is an icon helper held back while the panel is attached.
func (s *Supervisor) held(sl *slot) bool { return s.attached && sl.id != BridgeSlot }

// spawn starts a process for sl (s.mu held).
func (s *Supervisor) spawn(sl *slot, standby bool, now time.Time) (Proc, string, bool) {
	sl.gen++
	// a new process: what was asked of the last one is over
	sl.proc, sl.ready, sl.quitSent, sl.sentView, sl.standby = nil, false, false, false, standby
	sl.started = now
	token := newToken()
	p, err := s.o.Spawn(sl.id, UIDFor(sl.id), token)
	if err != nil {
		s.o.Log.Printf("tray helper %s: %v", name(sl.id), err)
		sl.standby = false
		return nil, "", false
	}
	return p, token, true
}

// launch says hello to sl's new process and follows it (s.mu held).
func (s *Supervisor) launch(sl *slot, p Proc, token string) {
	gen := sl.gen
	sl.proc = p
	sl.exited = make(chan struct{})
	sl.out = newSender(p.Stdin())
	hello := trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeHello, Token: token}
	if sl.id == BridgeSlot {
		// enable once is decided at each start (ruling F15)
		hello.Role = trayproto.RoleShell
		hello.EnableOnce = s.bridgeOnce && !s.bridgeEnabled && (s.o.BridgeEnableOnce == nil || s.o.BridgeEnableOnce())
	} else {
		hello.Provider, hello.UID, hello.Standby = string(sl.id), UIDFor(sl.id), sl.standby
		if s.o.IconDir != nil {
			hello.IconDir = s.o.IconDir(sl.id)
		}
		// icons left by helpers that crashed or were killed: this process
		// removes them first (a Windows notification icon outlives its
		// process until the pointer passes over it)
		hello.Stale, s.stale = s.stale, nil
	}
	sl.out.send(hello)
	exited := sl.exited
	go s.read(sl.id, gen, p)
	go func() {
		code, _ := p.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		close(exited)
		if cur := s.slots[sl.id]; cur == sl && sl.gen == gen {
			s.ended(sl, code, s.o.Clock.Now())
		} else {
			s.queue.put(Event{Provider: sl.id, Exit: &code, RestartIn: -1})
		}
	}()
}

// ended handles the end of sl's process (s.mu held).
func (s *Supervisor) ended(sl *slot, code int, now time.Time) {
	lived := now.Sub(sl.started)
	if sl.window != 0 && !(code == trayproto.ExitOK && sl.quitSent) {
		// it did not remove its icon itself
		s.stale = append(s.stale, trayproto.StaleIcon{Window: sl.window, UID: UIDFor(sl.id)})
		if n := len(s.stale); n > maxStale {
			s.stale = s.stale[n-maxStale:]
		}
	}
	sl.proc, sl.ready, sl.window = nil, false, 0
	if sl.out != nil {
		sl.out.close()
		sl.out = nil
	}
	ev := Event{Provider: sl.id, Exit: &code, RestartIn: -1}
	if !s.stop && sl.id != BridgeSlot && code == trayproto.ExitSessionLost {
		s.unpark() // a new session bus: its shell may be one the panel supports
	}
	switch {
	case s.stop:
		s.queue.put(ev)
		return
	case sl.wanted && !sl.quitSent && sl.id == BridgeSlot && code == trayproto.ExitUnsupported:
		sl.parked = true
		s.o.Log.Print("tray helper shell: no GNOME Shell the usage panel supports on this session bus; not started again until the next one")
		s.queue.put(ev)
		return
	case !sl.wanted:
		delete(s.slots, sl.id)
		s.queue.put(ev)
		return
	case sl.quitSent:
		// asked to quit, then wanted again before it ended, or a watcher
		// ended for a retry: start anew (start resets quitSent before it
		// spawns; a failed start goes to retry, which only sets a timer)
		s.queue.put(ev)
		s.start(sl)
		return
	case sl.standby:
		sl.standby = false
		// the try is planned before the exit is reported, so that whoever
		// acts on the report (a test moving a fake clock) finds it planned
		// (review m-5)
		if code == trayproto.ExitDesktopBack {
			s.probe(sl, "the desktop came back")
		} else {
			s.o.Log.Printf("tray helper %s: the watch for the desktop ended (exit %d); trying again at the planned time", name(sl.id), code)
		}
		s.queue.put(ev)
		return
	}
	s.retry(sl, ev, code, lived, now)
}

// unpark starts a parked bridge again, after the delay of a lost session
// (s.mu held).
func (s *Supervisor) unpark() {
	b := s.slots[BridgeSlot]
	if b == nil || !b.parked || !b.wanted || b.proc != nil {
		return
	}
	b.parked = false
	s.o.Log.Printf("tray helper shell: a new session bus; starting it again in %s", lostDelay)
	s.startIn(b, lostDelay)
}

// probe tries a stopped helper again, now or, when its last start was less
// than tryGap ago, then (s.mu held). It never starts more than one helper
// a minute, and after each failed try it waits twice as long, up to
// stoppedFor (review n-2: the view changes every minute).
func (s *Supervisor) probe(sl *slot, why string) {
	if s.stop || !sl.wanted || !sl.stopped || sl.proc != nil && !sl.standby || s.held(sl) {
		return
	}
	now := s.o.Clock.Now()
	if at := sl.lastStart.Add(tryGap(sl.tries)); now.Before(at) {
		if sl.probeAt.IsZero() {
			sl.probeAt = at
			timer := s.o.Clock.After(at.Sub(now))
			go func() {
				select {
				case <-timer:
				case <-s.done:
					return
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.slots[sl.id] == sl && sl.probeAt.Equal(at) {
					sl.probeAt = time.Time{}
					s.probe(sl, why)
				}
			}()
		}
		return
	}
	sl.probeAt = time.Time{}
	s.o.Log.Printf("tray helper %s: trying again (%s)", name(sl.id), why)
	if sl.proc != nil {
		s.quitOrKill(sl) // its watcher: the helper starts once it ended
		return
	}
	s.start(sl)
}

// retry plans the next start after an exit (or a failed start) of a
// wanted helper, and delivers ev with that plan (s.mu held). It never
// starts a helper itself.
func (s *Supervisor) retry(sl *slot, ev Event, code int, lived time.Duration, now time.Time) {
	if s.stop || !sl.wanted {
		s.queue.put(ev)
		return
	}
	trying := sl.trying
	sl.trying = false
	if lived >= healthyAfter {
		sl.consecutive, sl.tries, trying = 0, 0, false
	}
	var wait time.Duration
	switch {
	case code == trayproto.ExitSessionLost && lived >= quickLoss:
		wait, sl.tries = lostDelay, 0
	case code == trayproto.ExitSessionEnded:
		wait, sl.tries = endedDelay, 0
	default:
		sl.consecutive++
		kept := sl.failures[:0]
		for _, t := range sl.failures {
			if now.Sub(t) < failWindow {
				kept = append(kept, t)
			}
		}
		sl.failures = append(kept, now)
		switch {
		case trying:
			// a try of the stopped helper failed: stopped again at once
			sl.tries++
			s.o.Log.Printf("tray helper %s failed again (exit %d); trying again in %s at the soonest, %s at the latest",
				name(sl.id), code, tryGap(sl.tries), stoppedFor)
			sl.stopped = true
			wait = stoppedFor
		case len(sl.failures) >= failLimit:
			s.o.Log.Printf("tray helper %s stopped after repeated failures (exit %d); trying again in %s", name(sl.id), code, stoppedFor)
			sl.stopped, sl.tries = true, 0
			wait = stoppedFor
		default:
			sl.tries = 0
			wait = backoff[min(sl.consecutive-1, maxBackoffIdx)]
			s.o.Log.Printf("tray helper %s ended (exit %d); starting it again in %s", name(sl.id), code, wait)
		}
	}
	if code == trayproto.ExitSessionLost && lived >= quickLoss {
		s.o.Log.Printf("tray helper %s: the session bus went away; starting it again in %s", name(sl.id), wait)
	}
	if code == trayproto.ExitSessionEnded {
		s.o.Log.Printf("tray helper %s: the session is ending", name(sl.id))
	}
	ev.RestartIn, ev.Stopped = wait, sl.stopped
	s.startIn(sl, wait)
	s.queue.put(ev)
	if sl.stopped {
		s.startWatcher(sl)
	}
}

// startIn plans sl's next start after wait (s.mu held); any start before
// then voids it.
func (s *Supervisor) startIn(sl *slot, wait time.Duration) {
	sl.plan++
	plan := sl.plan
	timer := s.o.Clock.After(wait)
	go func() {
		select {
		case <-timer:
		case <-s.done:
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.slots[sl.id] != sl || sl.plan != plan {
			return // a start came first
		}
		if sl.stopped {
			sl.failures, sl.consecutive = nil, 0
		}
		if sl.proc != nil {
			s.quitOrKill(sl) // its watcher: the helper starts once it ended
			return
		}
		s.start(sl)
	}()
}

// read turns the helper's lines into events. Once it stops reading (the
// helper ended, or wrote what it may not) it closes the helper's output.
func (s *Supervisor) read(id provider.ID, gen int, p Proc) {
	out := p.Stdout()
	if c, ok := out.(io.Closer); ok {
		defer c.Close()
	}
	r := bufio.NewReader(out)
	for {
		m, err := trayproto.Decode(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				s.o.Log.Printf("tray helper %s: %v; ending it", name(id), err)
				p.Kill()
			}
			return
		}
		s.mu.Lock()
		sl := s.slots[id]
		current := sl != nil && sl.gen == gen && sl.proc == p && !sl.standby
		if !current && id == BridgeSlot && m.Type == trayproto.TypeShell && m.Enabled {
			// the extension was turned on, though the bridge has ended
			// before its line was read: still never asked again, and the
			// main process writes its marker (review m4)
			s.bridgeEnabled = true
			s.queue.put(Event{Provider: id, Enabled: true})
		}
		if current {
			switch {
			case id == BridgeSlot && m.Type == trayproto.TypeShell:
				if m.Enabled {
					s.bridgeEnabled = true // never asked again by this process (F15)
				}
				if m.Supported != nil && *m.Supported || m.Attached != nil && *m.Attached {
					sl.ready = true // on its bus with a supported shell: it can show the notice
					s.sendNotice()
				}
				s.queue.put(Event{Provider: id, Attached: m.Attached, Supported: m.Supported, Enabled: m.Enabled, Why: m.Why})
			case id == BridgeSlot && m.Type != trayproto.TypeClick:
				// the bridge shows no icon: nothing else it says counts
			case m.Type == trayproto.TypeReady:
				sl.ready = true
				if m.Window != 0 {
					sl.window = m.Window
				}
				s.queue.put(Event{Provider: id, Ready: true})
				s.sendNotice()
				// a tray icon came up: the desktop may be back for a
				// stopped helper too
				for _, other := range s.slots {
					if other != sl {
						s.probe(other, "another tray icon came up")
					}
				}
			case m.Type == trayproto.TypeWaiting:
				sl.ready = false
				s.queue.put(Event{Provider: id, Waiting: m.Why})
			case m.Type == trayproto.TypeClick:
				s.queue.put(Event{Provider: id, Click: m.ID})
			}
		}
		s.mu.Unlock()
	}
}

// quitOrKill asks sl's helper to quit and kills it when it has not ended
// stopTimeout later, as Stop does: a helper hung in the tray library
// would keep its icon, and a slot wanted again would wait for its end
// for ever (s.mu held).
func (s *Supervisor) quitOrKill(sl *slot) {
	if sl.proc == nil || sl.quitSent {
		return
	}
	s.quit(sl)
	p, exited := sl.proc, sl.exited
	timer := s.o.Clock.After(stopTimeout)
	go func() {
		select {
		case <-timer:
			s.o.Log.Printf("tray helper %s did not quit; ending it", name(sl.id))
			p.Kill()
		case <-exited:
		}
	}()
}

func (s *Supervisor) quit(sl *slot) {
	if !sl.quitSent && sl.out != nil {
		sl.quitSent = true
		sl.out.send(trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeQuit})
	}
}

func (s *Supervisor) sendView(sl *slot) {
	if sl.proc == nil || sl.quitSent || sl.standby || s.items == nil {
		return
	}
	icon := s.icons[sl.id]
	if sl.sentView && reflect.DeepEqual(sl.sentItems, s.items) && reflect.DeepEqual(sl.sentIcon, icon) &&
		reflect.DeepEqual(sl.sentPanel, s.panel) {
		return
	}
	ic := icon.Icon
	sl.out.send(trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeView, Icon: &ic, Title: icon.Title,
		Tooltip: icon.Tooltip, Items: s.items, Panel: s.panel})
	sl.sentItems, sl.sentIcon, sl.sentPanel, sl.sentView = s.items, icon, s.panel, true
}

// sendNotice hands a pending notice to a helper that can show it now, and
// it counts as taken then (review H3): while the panel is attached (or
// awaited) the bridge, once it said a supported shell answered or the
// panel attached (it shows it through the desktop's notifications, design
// 6.4); otherwise the first wanted icon helper whose icon is up (it shows
// it at once). A helper not there yet gets nothing: one that ends before
// it could show the notice does not take it, and none shows it twice. A
// notice the bridge had not taken when the panel stopped being attached
// goes to the icon helpers so (s.mu held).
func (s *Supervisor) sendNotice() {
	if s.notice == "" {
		return
	}
	var sl *slot
	if s.attached {
		sl = s.slots[BridgeSlot]
	} else {
		for _, id := range s.order {
			if c := s.slots[id]; c != nil && c.ready {
				sl = c
				break
			}
		}
	}
	if sl == nil || !sl.ready || sl.proc == nil || sl.out == nil || sl.quitSent || sl.standby {
		return
	}
	sl.out.send(trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeNotice, Text: s.notice})
	s.notice = ""
}

func name(id provider.ID) string {
	switch id {
	case "":
		return "none"
	case BridgeSlot:
		return "shell"
	}
	return string(id)
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sender writes one helper's lines in order on its own goroutine, so a
// helper that stops reading never blocks the agent; a newer view replaces
// one not yet written.
type sender struct {
	mu     sync.Mutex
	queue  []trayproto.Msg
	wake   chan struct{}
	closed bool
}

func newSender(w io.WriteCloser) *sender {
	sd := &sender{wake: make(chan struct{}, 1)}
	go func() {
		defer w.Close()
		for range sd.wake {
			for {
				sd.mu.Lock()
				if len(sd.queue) == 0 {
					closed := sd.closed
					sd.mu.Unlock()
					if closed {
						return
					}
					break
				}
				m := sd.queue[0]
				sd.queue = sd.queue[1:]
				sd.mu.Unlock()
				if err := trayproto.Encode(w, m); err != nil && !errors.Is(err, trayproto.ErrTooLong) {
					sd.mu.Lock()
					sd.queue, sd.closed = nil, true
					sd.mu.Unlock()
					return
				}
			}
		}
	}()
	return sd
}

func (sd *sender) send(m trayproto.Msg) {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	if sd.closed {
		return
	}
	if m.Type == trayproto.TypeView {
		for i, q := range sd.queue {
			if q.Type == trayproto.TypeView {
				sd.queue = slices.Delete(sd.queue, i, i+1)
				break
			}
		}
	}
	sd.queue = append(sd.queue, m)
	select {
	case sd.wake <- struct{}{}:
	default:
	}
}

// close ends the writer once the queue is written.
func (sd *sender) close() {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	if !sd.closed {
		sd.closed = true
		close(sd.wake)
	}
}

// eventQueue delivers events in order without ever blocking the
// supervisor (clicks are never dropped).
type eventQueue struct {
	mu   sync.Mutex
	q    []Event
	wake chan struct{}
	out  chan Event
}

func newEventQueue() *eventQueue {
	e := &eventQueue{wake: make(chan struct{}, 1), out: make(chan Event)}
	go func() {
		for range e.wake {
			for {
				e.mu.Lock()
				if len(e.q) == 0 {
					e.mu.Unlock()
					break
				}
				ev := e.q[0]
				e.q = e.q[1:]
				e.mu.Unlock()
				e.out <- ev
			}
		}
	}()
	return e
}

func (e *eventQueue) put(ev Event) {
	e.mu.Lock()
	e.q = append(e.q, ev)
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
