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
	probeGap      = time.Minute      // a stopped helper is tried at most this often besides its stop timer
	healthyAfter  = 10 * time.Minute // a helper that lived this long starts the backoff again
	quickLoss     = 10 * time.Second // a session loss sooner than this after start counts as a failure
	stopTimeout   = 3 * time.Second  // Stop waits this long for helpers to quit, then kills them
	maxBackoffIdx = 6
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
	notice string
	stop   bool
	done   chan struct{} // closed by Stop: pending restarts end
	queue  *eventQueue
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
	sentView    bool
	consecutive int         // failure exits in a row
	failures    []time.Time // failure exits within failWindow
	stopped     bool        // after failLimit failures, until a retry
	standby     bool        // the process is a watcher (stopped), not an icon
	lastStart   time.Time   // the last start of an icon helper (or its attempt)
	plan        int         // counts planned starts; a start voids older ones
	probeAt     time.Time   // a retry of the stopped helper is planned then
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
			sl.stopped, sl.failures, sl.consecutive = false, nil, 0
			if sl.proc != nil {
				s.quitOrKill(sl) // its watcher: the helper starts once it ended
			} else {
				s.start(sl) // a new start voids the pending retry
			}
		}
	}
}

// Show sets the view: the same menu for every helper, and each helper's
// own icon, title and tooltip. Only what changed is sent. A change tries a
// stopped helper again (at most once a minute): the user has no icon to
// click.
func (s *Supervisor) Show(items []trayproto.Item, icons map[provider.ID]IconView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := !reflect.DeepEqual(items, s.items) || !reflect.DeepEqual(icons, s.icons)
	s.items = slices.Clone(items)
	s.icons = icons
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
	sl.proc, sl.ready = nil, false
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
		s.queue.put(ev)
		if code == trayproto.ExitDesktopBack {
			s.probe(sl, "the desktop came back")
		} else {
			s.o.Log.Printf("tray helper %s: the watch for the desktop ended (exit %d); trying again at the planned time", name(sl.id), code)
		}
		return
	}
	s.retry(sl, ev, code, lived, now)
}

// probe tries a stopped helper again, now or, when its last start was less
// than probeGap ago, then (s.mu held). It never starts more than one
// helper a minute.
func (s *Supervisor) probe(sl *slot, why string) {
	if s.stop || !sl.wanted || !sl.stopped || sl.proc != nil && !sl.standby {
		return
	}
	now := s.o.Clock.Now()
	if at := sl.lastStart.Add(probeGap); now.Before(at) {
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
	if lived >= healthyAfter {
		sl.consecutive = 0
	}
	var wait time.Duration
	switch {
	case code == trayproto.ExitSessionLost && lived >= quickLoss:
		wait = lostDelay
	case code == trayproto.ExitSessionEnded:
		wait = endedDelay
	default:
		sl.consecutive++
		kept := sl.failures[:0]
		for _, t := range sl.failures {
			if now.Sub(t) < failWindow {
				kept = append(kept, t)
			}
		}
		sl.failures = append(kept, now)
		if len(sl.failures) >= failLimit {
			s.o.Log.Printf("tray helper %s stopped after repeated failures (exit %d); trying again in %s", name(sl.id), code, stoppedFor)
			sl.stopped = true
			wait = stoppedFor
		} else {
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
	if sl.sentView && reflect.DeepEqual(sl.sentItems, s.items) && reflect.DeepEqual(sl.sentIcon, icon) {
		return
	}
	ic := icon.Icon
	sl.out.send(trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeView, Icon: &ic, Title: icon.Title,
		Tooltip: icon.Tooltip, Items: s.items})
	sl.sentItems, sl.sentIcon, sl.sentView = s.items, icon, true
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
