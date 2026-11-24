package supervisor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// fakeProc is a helper process made of pipes: the test reads what the
// supervisor sends (got), writes the helper's lines (say) and ends it
// (exit). By default it exits 0 on quit, as a real helper does.
type fakeProc struct {
	id         provider.ID
	uid        uint32
	token      string
	inR        *io.PipeReader
	inW        *io.PipeWriter
	outR       *io.PipeReader
	outW       *io.PipeWriter
	got        chan trayproto.Msg
	exitCh     chan int
	once       sync.Once
	ignoreQuit bool
	killed     chan struct{}
}

func newFakeProc(id provider.ID, uid uint32, token string, ignoreQuit bool) *fakeProc {
	p := &fakeProc{id: id, uid: uid, token: token, got: make(chan trayproto.Msg, 64), exitCh: make(chan int, 1),
		ignoreQuit: ignoreQuit, killed: make(chan struct{})}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	go func() {
		r := bufio.NewReader(p.inR)
		for {
			m, err := trayproto.Decode(r)
			if err != nil {
				return
			}
			p.got <- m
			if m.Type == trayproto.TypeQuit && !p.ignoreQuit {
				p.exit(0)
			}
		}
	}()
	return p
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.inW }
func (p *fakeProc) Stdout() io.Reader     { return p.outR }
func (p *fakeProc) Wait() (int, error)    { return <-p.exitCh, nil }
func (p *fakeProc) Kill() {
	select {
	case <-p.killed:
	default:
		close(p.killed)
	}
	p.exit(-1)
}

func (p *fakeProc) exit(code int) {
	p.once.Do(func() {
		p.outW.Close()
		p.inR.Close()
		p.exitCh <- code
	})
}

func (p *fakeProc) say(t *testing.T, m trayproto.Msg) {
	t.Helper()
	m.V = trayproto.Version
	if err := trayproto.Encode(p.outW, m); err != nil {
		t.Fatal(err)
	}
}

// next is the next line the supervisor sent this helper.
func (p *fakeProc) next(t *testing.T) trayproto.Msg {
	t.Helper()
	select {
	case m := <-p.got:
		return m
	case <-time.After(5 * time.Second):
		t.Fatalf("helper %q: nothing sent", p.id)
		return trayproto.Msg{}
	}
}

// none: nothing more is sent to this helper for a while.
func (p *fakeProc) none(t *testing.T) {
	t.Helper()
	select {
	case m := <-p.got:
		t.Fatalf("helper %q: unexpected %+v", p.id, m)
	case <-time.After(50 * time.Millisecond):
	}
}

type harness struct {
	t          *testing.T
	clk        *clock.Fake
	sup        *Supervisor
	spawns     chan *fakeProc
	logBuf     *syncBuffer
	ignoreQuit bool
	failSpawn  bool
	mu         sync.Mutex
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, clk: clock.NewFake(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)), spawns: make(chan *fakeProc, 16), logBuf: &syncBuffer{}}
	h.sup = New(Options{
		Spawn: func(p provider.ID, uid uint32, token string) (Proc, error) {
			h.mu.Lock()
			fail, ignore := h.failSpawn, h.ignoreQuit
			h.mu.Unlock()
			if fail {
				return nil, errors.New("test: cannot start")
			}
			fp := newFakeProc(p, uid, token, ignore)
			h.spawns <- fp
			return fp, nil
		},
		IconDir: func(p provider.ID) string { return "/icons/" + name(p) },
		Clock:   h.clk,
		Log:     log.New(h.logBuf, "", 0),
	})
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { h.sup.Stop(); close(done) }()
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
				h.clk.Advance(time.Second) // a stuck helper is killed after 3 s
			}
		}
	})
	return h
}

// spawned is the next helper started, after its hello.
func (h *harness) spawned() *fakeProc {
	h.t.Helper()
	select {
	case p := <-h.spawns:
		m := p.next(h.t)
		if m.Type != trayproto.TypeHello || m.Token != p.token || p.token == "" || m.V != trayproto.Version {
			h.t.Fatalf("first line %+v (token %q)", m, p.token)
		}
		return p
	case <-time.After(5 * time.Second):
		h.t.Fatal("no helper started")
		return nil
	}
}

// noSpawn: no helper starts for a while.
func (h *harness) noSpawn() {
	h.t.Helper()
	select {
	case p := <-h.spawns:
		h.t.Fatalf("helper %q started", p.id)
	case <-time.After(50 * time.Millisecond):
	}
}

// event waits for the next event that matches.
func (h *harness) event(match func(Event) bool) Event {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-h.sup.Events():
			if match(e) {
				return e
			}
		case <-deadline:
			h.t.Fatal("event did not come")
			return Event{}
		}
	}
}

func isExit(e Event) bool { return e.Exit != nil }

func TestSpawnArguments(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	a, b := h.spawned(), h.spawned()
	if a.id != provider.Claude || a.uid != 100 || b.id != provider.Codex || b.uid != 101 {
		t.Fatalf("%q/%d %q/%d", a.id, a.uid, b.id, b.uid)
	}
	if a.token == b.token || len(a.token) < 32 {
		t.Fatalf("tokens %q %q", a.token, b.token)
	}
}

// What a helper is for comes in its first line, not on its command line
// (stage-2 review m-4).
func TestHelloSaysWhatTheHelperIsFor(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	for _, want := range []trayproto.Msg{
		{Provider: "claude", UID: 100, IconDir: "/icons/claude"},
		{Provider: "codex", UID: 101, IconDir: "/icons/codex"},
	} {
		p := <-h.spawns
		m := p.next(t)
		if m.Type != trayproto.TypeHello || m.Provider != want.Provider || m.UID != want.UID || m.IconDir != want.IconDir {
			t.Fatalf("hello %+v, want %+v", m, want)
		}
	}
	h.sup.Want(nil)
	p := <-h.spawns
	if m := p.next(t); m.Type != trayproto.TypeHello || m.Provider != "" || m.UID != 100 || m.IconDir != "/icons/none" {
		t.Fatalf("neutral hello %+v", m)
	}
}

func TestNeutralHelperWhenNoProvider(t *testing.T) {
	h := newHarness(t)
	h.sup.Want(nil)
	p := h.spawned()
	if p.id != "" || p.uid != 100 {
		t.Fatalf("%q %d", p.id, p.uid)
	}
	// the same set again changes nothing
	h.sup.Want([]provider.ID{})
	h.noSpawn()
	// a provider detected: the neutral helper is asked to quit, the
	// provider's helper starts
	h.sup.Want([]provider.ID{provider.Claude})
	c := h.spawned()
	if c.id != provider.Claude || c.uid != 100 {
		t.Fatalf("%q %d", c.id, c.uid)
	}
	if m := p.next(t); m.Type != trayproto.TypeQuit {
		t.Fatalf("neutral helper got %+v", m)
	}
	h.event(func(e Event) bool { return isExit(e) && e.Provider == "" })
	h.clk.Advance(time.Hour)
	h.noSpawn() // a helper that was asked to quit is not started again
}

func TestHelperRestartPolicy(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()

	// session lost (75) after a while: again after 1 s
	h.clk.Advance(time.Minute)
	p.exit(trayproto.ExitSessionLost)
	h.event(isExit)
	h.clk.Advance(999 * time.Millisecond)
	h.noSpawn()
	h.clk.Advance(time.Millisecond)
	p = h.spawned()

	// the session is ending (76): not at once; after 30 s (it did not end)
	h.clk.Advance(time.Minute)
	p.exit(trayproto.ExitSessionEnded)
	h.event(isExit)
	h.clk.Advance(29 * time.Second)
	h.noSpawn()
	h.clk.Advance(time.Second)
	p = h.spawned()

	// any other exit: 1, 2, 4, 8 s, then stopped at the fifth within 10 min
	h.clk.Advance(time.Minute)
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		p.exit(3)
		h.event(isExit)
		h.clk.Advance(wait - time.Millisecond)
		h.noSpawn()
		h.clk.Advance(time.Millisecond)
		p = h.spawned()
		_ = i
	}
	p.exit(3)
	h.event(isExit)
	h.clk.Advance(29 * time.Minute)
	h.noSpawn()
	if !strings.Contains(h.logBuf.String(), "tray helper claude stopped after repeated failures") {
		t.Fatalf("log: %s", h.logBuf.String())
	}
	h.clk.Advance(time.Minute) // 30 minutes later it is tried again
	p = h.spawned()

	// the count starts again: the next failure waits 1 s
	p.exit(3)
	h.event(isExit)
	h.clk.Advance(time.Second)
	p = h.spawned()

	// a session loss right after start counts as a failure (no 1 s loop)
	p.exit(trayproto.ExitSessionLost)
	h.event(isExit)
	h.clk.Advance(time.Second)
	h.noSpawn()
	h.clk.Advance(time.Second)
	_ = h.spawned()
}

func TestBackoffGrowsToAMinute(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	// failures spread out so that fewer than five fall in any 10 minutes
	var waits []time.Duration
	for range 8 {
		h.clk.Advance(3 * time.Minute)
		p.exit(1)
		e := h.event(isExit)
		waits = append(waits, e.RestartIn)
		h.clk.Advance(e.RestartIn - time.Millisecond)
		h.noSpawn()
		h.clk.Advance(time.Millisecond)
		p = h.spawned()
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i := range want {
		want[i] *= time.Second
	}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits %v", waits)
	}
	// a helper that ran 10 minutes starts the count again
	h.clk.Advance(10 * time.Minute)
	p.exit(1)
	h.event(isExit)
	h.clk.Advance(time.Second)
	_ = h.spawned()
}

func TestStoppedHelperRetriesAtTheNextWantChange(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	for range 5 {
		p.exit(1)
		h.event(isExit)
		if !strings.Contains(h.logBuf.String(), "stopped after repeated failures") {
			h.clk.Advance(time.Minute)
			p = h.spawned()
		}
	}
	h.noSpawn()
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	ids := map[provider.ID]bool{h.spawned().id: true, h.spawned().id: true}
	if !ids[provider.Claude] || !ids[provider.Codex] {
		t.Fatalf("%v", ids)
	}
}

func TestSpawnFailureIsRetried(t *testing.T) {
	h := newHarness(t)
	h.failSpawn = true
	h.sup.Want([]provider.ID{provider.Claude})
	h.event(isExit)
	h.mu.Lock()
	h.failSpawn = false
	h.mu.Unlock()
	h.clk.Advance(time.Second)
	_ = h.spawned()
	if !strings.Contains(h.logBuf.String(), "cannot start") {
		t.Fatalf("log: %s", h.logBuf.String())
	}
}

func TestViewsAreSentWhenTheyChange(t *testing.T) {
	h := newHarness(t)
	items := []trayproto.Item{{ID: "quit", Text: "Quit", Enabled: true, Visible: true}}
	icons := map[provider.ID]IconView{
		provider.Claude: {Icon: trayproto.Icon{Provider: "claude", Level: "normal"}, Title: "42%", Tooltip: "AI Usage · Claude"},
		provider.Codex:  {Icon: trayproto.Icon{Provider: "codex", Level: "normal"}, Title: "17%", Tooltip: "AI Usage · Codex"},
	}
	h.sup.Show(items, icons) // before any helper: kept for the first ones
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	a, b := h.spawned(), h.spawned()
	for _, p := range []*fakeProc{a, b} {
		m := p.next(t)
		if m.Type != trayproto.TypeView || !reflect.DeepEqual(m.Items, items) || m.Icon.Provider != string(p.id) || m.Title != icons[p.id].Title || m.Tooltip != icons[p.id].Tooltip {
			t.Fatalf("%q got %+v", p.id, m)
		}
	}
	h.sup.Show(items, icons) // nothing changed
	a.none(t)
	b.none(t)
	// only Codex's icon changed: only its helper hears it
	icons2 := map[provider.ID]IconView{provider.Claude: icons[provider.Claude], provider.Codex: {Icon: trayproto.Icon{Provider: "codex", Level: "critical"}, Title: "100%"}}
	h.sup.Show(items, icons2)
	if m := b.next(t); m.Title != "100%" {
		t.Fatalf("%+v", m)
	}
	a.none(t)
	// the menu changed: both
	items2 := []trayproto.Item{{ID: "quit", Text: "Quit now", Enabled: true, Visible: true}}
	h.sup.Show(items2, icons2)
	if m := a.next(t); m.Items[0].Text != "Quit now" {
		t.Fatalf("%+v", m)
	}
	if m := b.next(t); m.Items[0].Text != "Quit now" {
		t.Fatalf("%+v", m)
	}
	// a restarted helper gets the current view at once
	h.clk.Advance(time.Minute)
	a.exit(trayproto.ExitSessionLost)
	h.event(isExit)
	h.clk.Advance(time.Second)
	a = h.spawned()
	if m := a.next(t); m.Type != trayproto.TypeView || m.Items[0].Text != "Quit now" {
		t.Fatalf("%+v", m)
	}
}

func TestEventsAndAnyReady(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	a, b := h.spawned(), h.spawned()
	if h.sup.AnyReady() {
		t.Fatal("ready before any helper said so")
	}
	b.say(t, trayproto.Msg{Type: trayproto.TypeWaiting, Why: "no StatusNotifierWatcher"})
	if e := h.event(func(e Event) bool { return e.Waiting != "" }); e.Provider != provider.Codex || e.Waiting != "no StatusNotifierWatcher" {
		t.Fatalf("%+v", e)
	}
	a.say(t, trayproto.Msg{Type: trayproto.TypeReady})
	if e := h.event(func(e Event) bool { return e.Ready }); e.Provider != provider.Claude {
		t.Fatalf("%+v", e)
	}
	if !h.sup.AnyReady() {
		t.Fatal("not ready")
	}
	b.say(t, trayproto.Msg{Type: trayproto.TypeClick, ID: "refresh"})
	if e := h.event(func(e Event) bool { return e.Click != "" }); e.Provider != provider.Codex || e.Click != "refresh" {
		t.Fatalf("%+v", e)
	}
	a.exit(1)
	e := h.event(isExit)
	if e.Provider != provider.Claude || *e.Exit != 1 {
		t.Fatalf("%+v", e)
	}
	if h.sup.AnyReady() {
		t.Fatal("still ready after the ready helper ended")
	}
}

func TestGarbageFromAHelperEndsIt(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	if _, err := io.WriteString(p.outW, "this is not a message\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("not killed")
	}
}

func TestNoticeGoesToTheFirstHelper(t *testing.T) {
	h := newHarness(t)
	h.sup.Notice("AI Usage is running.")
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	a, b := h.spawned(), h.spawned()
	if m := a.next(t); m.Type != trayproto.TypeNotice || m.Text != "AI Usage is running." {
		t.Fatalf("%+v", m)
	}
	b.none(t)
	// once only: a restarted first helper does not show it again
	h.clk.Advance(time.Minute)
	a.exit(trayproto.ExitSessionLost)
	h.event(isExit)
	h.clk.Advance(time.Second)
	a = h.spawned()
	a.none(t)
	// a notice while the helper runs is sent at once
	h.sup.Notice("second")
	if m := a.next(t); m.Text != "second" {
		t.Fatalf("%+v", m)
	}
}

func TestStopKillsAfterTimeout(t *testing.T) {
	h := newHarness(t)
	h.ignoreQuit = true
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	done := make(chan struct{})
	go func() { h.sup.Stop(); close(done) }()
	if m := p.next(t); m.Type != trayproto.TypeQuit {
		t.Fatalf("%+v", m)
	}
	// wait until Stop waits on the clock, then let 3 s pass
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.clk.Advance(500 * time.Millisecond)
		select {
		case <-done:
		case <-time.After(10 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("Stop did not return")
			}
			continue
		}
		break
	}
	select {
	case <-p.killed:
	default:
		t.Fatal("not killed")
	}
	// nothing starts after Stop
	h.sup.Want([]provider.ID{provider.Codex})
	h.clk.Advance(time.Hour)
	h.noSpawn()
}

func TestStopEndsAPendingRestart(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	p.exit(3)
	h.event(isExit)
	h.sup.Stop()
	h.clk.Advance(time.Hour)
	h.noSpawn()
}

func TestQuitHelperIsNotRestarted(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	a, b := h.spawned(), h.spawned()
	h.sup.Want([]provider.ID{provider.Claude})
	if m := b.next(t); m.Type != trayproto.TypeQuit {
		t.Fatalf("%+v", m)
	}
	if e := h.event(func(e Event) bool { return isExit(e) && e.Provider == provider.Codex }); e.RestartIn >= 0 {
		t.Fatalf("restart planned: %+v", e)
	}
	h.clk.Advance(time.Hour)
	h.noSpawn()
	a.none(t)
}

// A helper no longer wanted that ignores quit (hung in the tray library)
// is killed after 3 s, as Stop does, so its provider can get a working
// icon again (stage-2 review m-2).
func TestDroppedHelperThatIgnoresQuitIsKilled(t *testing.T) {
	h := newHarness(t)
	h.ignoreQuit = true
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	h.sup.Want([]provider.ID{provider.Codex})
	_ = h.spawned()
	if m := p.next(t); m.Type != trayproto.TypeQuit {
		t.Fatalf("%+v", m)
	}
	h.clk.Advance(stopTimeout - time.Millisecond)
	select {
	case <-p.killed:
		t.Fatal("killed before 3 s")
	case <-time.After(50 * time.Millisecond):
	}
	h.clk.Advance(time.Millisecond)
	select {
	case <-p.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("not killed")
	}
	h.event(func(e Event) bool { return isExit(e) && e.Provider == provider.Claude })
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	if c := h.spawned(); c.id != provider.Claude {
		t.Fatalf("%q", c.id)
	}
}

// Wanted again while it ignores the quit: it is killed and a new one
// starts.
func TestRewantedHelperThatIgnoresQuitIsReplaced(t *testing.T) {
	h := newHarness(t)
	h.ignoreQuit = true
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	h.sup.Want([]provider.ID{provider.Codex})
	_ = h.spawned()
	h.sup.Want([]provider.ID{provider.Claude})
	h.clk.Advance(stopTimeout)
	select {
	case <-p.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("not killed")
	}
	if c := h.spawned(); c.id != provider.Claude {
		t.Fatalf("%q", c.id)
	}
}

// A helper that ends on quit is not killed later.
func TestKillTimerEndsWithTheHelper(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	p := h.spawned()
	h.sup.Want([]provider.ID{provider.Codex})
	_ = h.spawned()
	h.event(func(e Event) bool { return isExit(e) && e.Provider == provider.Claude })
	h.clk.Advance(time.Hour)
	select {
	case <-p.killed:
		t.Fatal("killed after it ended")
	case <-time.After(50 * time.Millisecond):
	}
}
