package supervisor

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// started is the next helper started and its hello.
func (h *harness) started() (*fakeProc, trayproto.Msg) {
	h.t.Helper()
	select {
	case p := <-h.spawns:
		m := p.next(h.t)
		if m.Type != trayproto.TypeHello || m.Token != p.token || p.token == "" {
			h.t.Fatalf("first line %+v (token %q)", m, p.token)
		}
		return p, m
	case <-time.After(5 * time.Second):
		h.t.Fatal("no helper started")
		return nil, trayproto.Msg{}
	}
}

// bridge is the next helper started, which must be the bridge.
func (h *harness) bridge() (*fakeProc, trayproto.Msg) {
	h.t.Helper()
	p, m := h.started()
	if p.id != BridgeSlot || m.Role != trayproto.RoleShell {
		h.t.Fatalf("helper %q started with %+v, want the bridge", p.id, m)
	}
	return p, m
}

func isExitOf(id provider.ID) func(Event) bool {
	return func(e Event) bool { return e.Exit != nil && e.Provider == id }
}

func quitGot(t *testing.T, p *fakeProc) {
	t.Helper()
	for {
		m := p.next(t)
		if m.Type == trayproto.TypeQuit {
			return
		}
		if m.Type != trayproto.TypeView && m.Type != trayproto.TypeNotice {
			t.Fatalf("helper %q got %+v, want quit", p.id, m)
		}
	}
}

// Attached, no icon helper runs (design 6.4): each is asked to quit (and
// killed when it does not), none is started again, whatever ends it, and
// a change of the wanted helpers (the neutral one too) starts none; once
// detached, Want's helpers start at once.
func TestAttachedMeansNoIconHelpers(t *testing.T) {
	h := newHarness(t)
	h.ignoreQuit = true
	h.sup.Want([]provider.ID{provider.Claude, provider.Codex})
	a, b := h.spawned(), h.spawned()
	if a.id != provider.Claude {
		a, b = b, a
	}
	h.sup.WantBridge(true, false)
	br, _ := h.bridge()
	h.clk.Advance(time.Minute)
	h.sup.SetPanelAttached(true)
	quitGot(t, a)
	quitGot(t, b)
	br.none(t)                        // the bridge stays
	a.exit(trayproto.ExitSessionLost) // ends by itself meanwhile: not started again
	h.event(isExitOf(provider.Claude))
	h.clk.Advance(stopTimeout) // the other does not quit: killed
	select {
	case <-b.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("not killed")
	}
	h.event(isExitOf(provider.Codex))
	h.clk.Advance(time.Hour)
	h.noSpawn()
	h.sup.Want(nil) // the neutral helper: not either
	h.noSpawn()
	h.sup.Show(view("41%"))
	h.clk.Advance(time.Hour)
	h.noSpawn()
	if h.sup.AnyReady() {
		t.Fatal("ready without an icon helper")
	}
	h.sup.Want([]provider.ID{provider.Codex})
	h.noSpawn()
	h.sup.SetPanelAttached(true) // again: nothing
	h.noSpawn()
	h.sup.SetPanelAttached(false)
	if p := h.spawned(); p.id != provider.Codex {
		t.Fatalf("%q started", p.id)
	}
	h.noSpawn()
	notQuit(t, br)
}

// notQuit: p is not asked to quit for a while (views may come).
func notQuit(t *testing.T, p *fakeProc) {
	t.Helper()
	deadline := time.After(50 * time.Millisecond)
	for {
		select {
		case m := <-p.got:
			if m.Type == trayproto.TypeQuit {
				t.Fatalf("helper %q asked to quit", p.id)
			}
		case <-deadline:
			return
		}
	}
}

// A helper stopped after repeated failures before the panel attached
// starts again at once when it goes away (Want's set runs again).
func TestDetachStartsAStoppedHelper(t *testing.T) {
	h := newHarnessWith(t, true)
	h.sup.Want([]provider.ID{provider.Claude})
	h.failUntilStopped(h.spawned())
	w, hello := h.started() // its watcher
	if !hello.Standby {
		t.Fatalf("%+v", hello)
	}
	h.sup.SetPanelAttached(true)
	quitGot(t, w)
	h.event(isExitOf(provider.Claude))
	h.clk.Advance(stoppedFor) // its stop timer is void while attached
	h.noSpawn()
	h.sup.SetPanelAttached(false)
	if _, m := h.started(); m.Standby || m.Provider != "claude" {
		t.Fatalf("%+v", m)
	}
}

// The bridge's hello carries the role and nothing of an icon; enable once
// is decided at each start (ruling F15): by WantBridge, by
// Options.BridgeEnableOnce asked then, and never again once a bridge said
// it turned the extension on.
func TestBridgeHelloEnableOnceAtEachStart(t *testing.T) {
	var allow atomic.Bool
	allow.Store(true)
	h := newHarnessFor(t, func(o *Options) { o.BridgeEnableOnce = allow.Load })
	h.sup.Want([]provider.ID{provider.Claude})
	_ = h.spawned()
	h.sup.WantBridge(true, true)
	b, m := h.bridge()
	if !m.EnableOnce || m.Provider != "" || m.UID != 0 || m.IconDir != "" || m.Standby || m.Stale != nil {
		t.Fatalf("bridge hello %+v", m)
	}
	restart := func() trayproto.Msg {
		t.Helper()
		h.clk.Advance(time.Minute)
		b.exit(trayproto.ExitSessionLost)
		h.event(isExitOf(BridgeSlot))
		h.clk.Advance(lostDelay)
		var m trayproto.Msg
		b, m = h.bridge()
		return m
	}
	allow.Store(false) // the marker was written meanwhile
	if m := restart(); m.EnableOnce {
		t.Fatalf("%+v", m)
	}
	allow.Store(true)
	if m := restart(); !m.EnableOnce {
		t.Fatalf("%+v", m)
	}
	b.say(t, trayproto.Msg{Type: trayproto.TypeShell, Enabled: true})
	if e := h.event(func(e Event) bool { return e.Enabled }); e.Provider != BridgeSlot {
		t.Fatalf("%+v", e)
	}
	if m := restart(); m.EnableOnce { // spent, whatever the marker says
		t.Fatalf("%+v", m)
	}
	// WantBridge's own say
	h2 := newHarness(t)
	h2.sup.WantBridge(true, false)
	if _, m := h2.bridge(); m.EnableOnce {
		t.Fatalf("%+v", m)
	}
}

// The bridge's lines become events; it shows no icon, so nothing else it
// says counts; an icon helper's shell line is ignored.
func TestBridgeShellLinesAreEvents(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	c := h.spawned()
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	yes, no := true, false
	c.say(t, trayproto.Msg{Type: trayproto.TypeShell, Attached: &yes})
	b.say(t, trayproto.Msg{Type: trayproto.TypeReady})
	b.say(t, trayproto.Msg{Type: trayproto.TypeShell, Attached: &yes})
	e := h.event(func(e Event) bool { return e.Attached != nil })
	if e.Provider != BridgeSlot || !*e.Attached {
		t.Fatalf("%+v", e)
	}
	if h.sup.AnyReady() {
		t.Fatal("the bridge counts as an icon")
	}
	b.say(t, trayproto.Msg{Type: trayproto.TypeShell, Attached: &no, Why: "ping"})
	if e := h.event(func(e Event) bool { return e.Attached != nil }); *e.Attached || e.Why != "ping" {
		t.Fatalf("%+v", e)
	}
	b.say(t, trayproto.Msg{Type: trayproto.TypeClick, ID: "r.claude.0"})
	if e := h.event(func(e Event) bool { return e.Click != "" }); e.Provider != BridgeSlot || e.Click != "r.claude.0" {
		t.Fatalf("%+v", e)
	}
	b.say(t, trayproto.Msg{Type: trayproto.TypeShell, Supported: &no, Why: "unsupported 44.2"})
	if e := h.event(func(e Event) bool { return e.Supported != nil }); *e.Supported || e.Why != "unsupported 44.2" {
		t.Fatalf("%+v", e)
	}
}

// Want never touches the bridge's slot; Show sends it the view (it serves
// the panel).
func TestWantLeavesTheBridgeAlone(t *testing.T) {
	h := newHarness(t)
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	h.sup.Want([]provider.ID{provider.Claude})
	_ = h.spawned()
	h.sup.Want(nil)
	_ = h.spawned()
	b.none(t)
	items, icons, _ := view("40%")
	panel := &trayproto.Panel{Serial: 3, Server: "Server connected"}
	h.sup.Show(items, icons, panel)
	if m := b.next(t); m.Type != trayproto.TypeView || m.Panel == nil || m.Panel.Serial != 3 {
		t.Fatalf("%+v", m)
	}
	h.sup.WantBridge(false, false)
	quitGot(t, b)
	h.event(isExitOf(BridgeSlot))
	h.clk.Advance(time.Hour)
	h.noSpawn()
}

// Exit 78 (no supported GNOME Shell, plan PR-10) parks the bridge: no
// restart, not for an hour, not for a new view; an icon helper's exit 75
// (a new session bus) starts it once; so does WantBridge off and on.
func TestUnsupportedShellParksTheBridgeUntilSessionLost(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	c := h.spawned()
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	b.exit(trayproto.ExitUnsupported)
	if e := h.event(isExitOf(BridgeSlot)); e.RestartIn >= 0 || e.Stopped {
		t.Fatalf("%+v", e)
	}
	h.clk.Advance(time.Hour)
	h.noSpawn()
	h.sup.Show(view("41%"))
	h.sup.WantBridge(true, false) // on again without off: still parked
	h.clk.Advance(time.Hour)
	h.noSpawn()
	if !strings.Contains(h.logBuf.String(), "not started again until the next one") {
		t.Fatalf("log: %s", h.logBuf.String())
	}
	c.exit(trayproto.ExitSessionLost)
	h.event(isExitOf(provider.Claude))
	h.clk.Advance(lostDelay)
	got := map[provider.ID]int{}
	for range 2 {
		p, _ := h.started()
		got[p.id]++
		if p.id == BridgeSlot {
			b = p
		} else {
			c = p
		}
	}
	if got[BridgeSlot] != 1 || got[provider.Claude] != 1 {
		t.Fatalf("started %v", got)
	}
	h.clk.Advance(time.Hour)
	h.noSpawn()
	// parked again; WantBridge off and on starts it at once
	b.exit(trayproto.ExitUnsupported)
	h.event(isExitOf(BridgeSlot))
	h.sup.WantBridge(false, false)
	h.noSpawn()
	h.sup.WantBridge(true, false)
	_, _ = h.bridge()
}

// The bridge follows the helpers' restart rules, but a stopped bridge gets
// no standby watcher (it has no tray to watch) and its exit says Stopped.
func TestStoppedBridgeIsNotWatched(t *testing.T) {
	h := newHarnessWith(t, true)
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		b.exit(3)
		if e := h.event(isExitOf(BridgeSlot)); e.Stopped || e.RestartIn != wait {
			t.Fatalf("%+v", e)
		}
		h.clk.Advance(wait)
		b, _ = h.bridge()
	}
	b.exit(3)
	if e := h.event(isExitOf(BridgeSlot)); !e.Stopped || e.RestartIn != stoppedFor {
		t.Fatalf("%+v", e)
	}
	h.noSpawn() // no watcher
	h.clk.Advance(stoppedFor)
	_, _ = h.bridge()
}

// While attached the notice goes to the bridge (it shows it through the
// desktop's notifications); otherwise to the first icon helper as before.
func TestNoticeGoesToBridgeWhenAttached(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	c := h.spawned()
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	h.sup.SetPanelAttached(true)
	quitGot(t, c)
	h.event(isExitOf(provider.Claude))
	h.sup.Notice("AI Usage is running.")
	if m := b.next(t); m.Type != trayproto.TypeNotice || m.Text != "AI Usage is running." {
		t.Fatalf("%+v", m)
	}
	h.sup.SetPanelAttached(false)
	c = h.spawned()
	h.sup.Notice("second")
	if m := c.next(t); m.Type != trayproto.TypeNotice || m.Text != "second" {
		t.Fatalf("%+v", m)
	}
	b.none(t)
	// a notice pending when the panel attaches goes to the bridge then
	h2 := newHarness(t)
	h2.sup.WantBridge(true, false)
	b2, _ := h2.bridge()
	h2.sup.Notice("third") // no icon helper yet
	b2.none(t)
	h2.sup.SetPanelAttached(true)
	if m := b2.next(t); m.Text != "third" {
		t.Fatalf("%+v", m)
	}
}

// Stop ends the bridge too, and nothing starts after it.
func TestStopEndsTheBridgeToo(t *testing.T) {
	h := newHarness(t)
	h.sup.Want([]provider.ID{provider.Claude})
	c := h.spawned()
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	h.sup.Stop()
	for _, p := range []*fakeProc{c, b} {
		quitGot(t, p)
	}
	h.sup.WantBridge(false, false)
	h.sup.WantBridge(true, false)
	h.sup.SetPanelAttached(true)
	h.sup.SetPanelAttached(false)
	h.clk.Advance(time.Hour)
	h.noSpawn()
}

// gatedProc is a helper whose output is read only once gate is closed:
// its last line can come after its end (review m4).
type gatedProc struct {
	*fakeProc
	out io.Reader
}

func (p *gatedProc) Stdout() io.Reader { return p.out }

type gatedReader struct {
	gate <-chan struct{}
	r    io.Reader
}

func (g gatedReader) Read(b []byte) (int, error) {
	<-g.gate
	return g.r.Read(b)
}

// Review m4: a bridge that says enabled and ends at once (its line read
// after its end was handled) still spends the enable-once: the event
// comes, and the next bridge's hello does not ask again.
func TestEnabledLineAfterTheBridgeEnded(t *testing.T) {
	gate := make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	var h *harness
	h = newHarnessFor(t, func(o *Options) {
		o.Spawn = func(p provider.ID, uid uint32, token string) (Proc, error) {
			fp := newFakeProc(p, uid, token, false)
			h.spawns <- fp
			if p == BridgeSlot && first.Swap(false) {
				return &gatedProc{fp, gatedReader{gate, strings.NewReader(`{"v":1,"type":"shell","enabled":true}` + "\n")}}, nil
			}
			return fp, nil
		}
	})
	h.sup.WantBridge(true, true)
	b, m := h.bridge()
	if !m.EnableOnce {
		t.Fatalf("%+v", m)
	}
	b.exit(trayproto.ExitSessionLost)
	h.event(isExitOf(BridgeSlot))
	close(gate)
	if e := h.event(func(e Event) bool { return e.Enabled }); e.Provider != BridgeSlot {
		t.Fatalf("%+v", e)
	}
	h.clk.Advance(lostDelay)
	if _, m := h.bridge(); m.EnableOnce {
		t.Fatalf("asked to turn the extension on again: %+v", m)
	}
}

// Review m5: a notice handed to a helper that ends before it took it (a
// bridge that could not dial its bus says nothing) goes to the next
// helper; once a helper took it (the bridge said something, an icon
// helper's icon came up) it is not shown again.
func TestNoticeKeptUntilAHelperTookIt(t *testing.T) {
	h := newHarness(t)
	h.sup.WantBridge(true, false)
	b, _ := h.bridge()
	h.sup.SetPanelAttached(true)
	h.sup.Notice("AI Usage is running.")
	if m := b.next(t); m.Type != trayproto.TypeNotice {
		t.Fatalf("%+v", m)
	}
	b.exit(trayproto.ExitSessionLost) // no bus: it said nothing
	h.event(isExitOf(BridgeSlot))
	h.clk.Advance(lostDelay)
	b, _ = h.bridge()
	if m := b.next(t); m.Type != trayproto.TypeNotice || m.Text != "AI Usage is running." {
		t.Fatalf("%+v", m)
	}
	yes := true
	b.say(t, trayproto.Msg{Type: trayproto.TypeShell, Supported: &yes})
	h.event(func(e Event) bool { return e.Supported != nil })
	b.exit(trayproto.ExitSessionLost)
	h.event(isExitOf(BridgeSlot))
	h.clk.Advance(2 * lostDelay)
	b, _ = h.bridge()
	b.none(t)
	// an icon helper that ends before its icon came up hands it on too
	h2 := newHarness(t)
	h2.sup.Notice("second")
	h2.sup.Want([]provider.ID{provider.Claude})
	c := h2.spawned()
	if m := c.next(t); m.Text != "second" {
		t.Fatalf("%+v", m)
	}
	c.exit(trayproto.ExitSessionLost)
	h2.event(isExit)
	h2.clk.Advance(lostDelay)
	c = h2.spawned()
	if m := c.next(t); m.Text != "second" {
		t.Fatalf("%+v", m)
	}
	c.say(t, trayproto.Msg{Type: trayproto.TypeReady})
	h2.event(func(e Event) bool { return e.Ready })
	h2.clk.Advance(time.Minute)
	c.exit(trayproto.ExitSessionLost)
	h2.event(isExit)
	h2.clk.Advance(lostDelay)
	h2.spawned().none(t)
}
