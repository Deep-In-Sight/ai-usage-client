// Package supervisor runs the tray helpers: one `ai-usage tray-helper`
// process per provider in use (design D3, 6.4), each showing one tray
// icon. The main process keeps collecting and syncing whatever happens to
// them; the supervisor starts a helper again when it ends (sooner for a
// lost session, later for repeated failures) and stops them all when the
// agent stops. It talks to them only through their standard input and
// output (package trayproto).
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
	Clock   clock.Clock
	Log     *log.Logger
}

// Event is what happened to a helper.
type Event struct {
	Provider provider.ID
	Click    string // an item was clicked: its id
	Ready    bool   // the helper's icon is up
	Waiting  string // the helper's icon is not up, and why
	Exit     *int   // the helper ended with this code
	// RestartIn (with Exit) is when it is started again; negative: not.
	RestartIn time.Duration
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
	o      Options
	mu     sync.Mutex
	order  []provider.ID
	slots  map[provider.ID]*slot
	items  []trayproto.Item
	icons  map[provider.ID]IconView
	panel  *trayproto.Panel
	notice string
	stop   bool
	done   chan struct{} // closed by Stop: pending restarts end
	queue  *eventQueue
	// stale: Windows icons of helpers that ended without removing them
	// (a crash, a kill); the next helper started removes them
	stale []trayproto.StaleIcon
}

type slot struct {
	id          provider.ID
	wanted      bool
	gen         int // counts starts; a stale exit or timer is ignored
	proc        Proc
	out         *sender
	ready       bool
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
		if !slices.Contains(ids, id) {
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

// Show sets the view: the same menu and usage panel for every helper
// (design 6.5: a Linux icon helper ignores the panel), and each helper's
// own icon, title and tooltip. Only what changed is sent. A change tries a
// stopped helper again (at most once a minute): the user has no icon to
// click. panel (nil: none) is not changed after the call.
func (s *Supervisor) Show(items []trayproto.Item, icons map[provider.ID]IconView, panel *trayproto.Panel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := !reflect.DeepEqual(items, s.items) || !reflect.DeepEqual(icons, s.icons) || !reflect.DeepEqual(panel, s.panel)
	s.items = slices.Clone(items)
	s.icons = icons
	s.panel = panel
	for _, sl := range s.slots {
		s.sendView(sl)
		if changed {
			s.probe(sl, "what it shows changed")
		}
	}
}

// Notice is shown once, by the first helper (when its icon is up).
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
	for _, sl := range s.slots {
		if sl.ready {
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
	if s.stop || !sl.wanted {
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
	s.sendNotice() // before the view: shown as soon as the icon is up
	s.sendView(sl)
}

// startWatcher starts sl's helper in standby, to watch the desktop while
// the helper is stopped (s.mu held). A failed start is logged only: the
// stop timer stays.
func (s *Supervisor) startWatcher(sl *slot) {
	if s.stop || !sl.wanted || !s.o.Standby || sl.proc != nil {
		return
	}
	if p, token, ok := s.spawn(sl, true, s.o.Clock.Now()); ok {
		s.o.Log.Printf("tray helper %s: watching the desktop for its tray to come back", name(sl.id))
		s.launch(sl, p, token)
	}
}

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
	hello := trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeHello, Token: token, Provider: string(sl.id),
		UID: UIDFor(sl.id), Standby: sl.standby}
	if s.o.IconDir != nil {
		hello.IconDir = s.o.IconDir(sl.id)
	}
	// icons left by helpers that crashed or were killed: this process
	// removes them first (a Windows notification icon outlives its process
	// until the pointer passes over it)
	hello.Stale, s.stale = s.stale, nil
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
	switch {
	case s.stop:
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

// probe tries a stopped helper again, now or, when its last start was less
// than tryGap ago, then (s.mu held). It never starts more than one helper
// a minute, and after each failed try it waits twice as long, up to
// stoppedFor (review n-2: the view changes every minute).
func (s *Supervisor) probe(sl *slot, why string) {
	if s.stop || !sl.wanted || !sl.stopped || sl.proc != nil && !sl.standby {
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
	ev.RestartIn = wait
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
	s.queue.put(ev)
	if sl.stopped {
		s.startWatcher(sl)
	}
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
		if current {
			switch m.Type {
			case trayproto.TypeReady:
				sl.ready = true
				if m.Window != 0 {
					sl.window = m.Window
				}
				s.queue.put(Event{Provider: id, Ready: true})
				// a tray icon came up: the desktop may be back for a
				// stopped helper too
				for _, other := range s.slots {
					if other != sl {
						s.probe(other, "another tray icon came up")
					}
				}
			case trayproto.TypeWaiting:
				sl.ready = false
				s.queue.put(Event{Provider: id, Waiting: m.Why})
			case trayproto.TypeClick:
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

// sendNotice hands a pending notice to the first wanted helper.
func (s *Supervisor) sendNotice() {
	if s.notice == "" || len(s.order) == 0 {
		return
	}
	sl := s.slots[s.order[0]]
	if sl == nil || sl.proc == nil || sl.quitSent || sl.standby {
		return
	}
	sl.out.send(trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeNotice, Text: s.notice})
	s.notice = ""
}

func name(id provider.ID) string {
	if id == "" {
		return "none"
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
