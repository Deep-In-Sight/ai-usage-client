package cli

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// rigProc is one helper process of a loopRig, made of pipes: the test
// reads its hello and what it got, writes its lines (say) and ends it
// (exit). It ends with 0 on quit, as a real helper does.
type rigProc struct {
	id        provider.ID
	hello     trayproto.Msg
	got       chan trayproto.Msg
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	exitCh    chan int
	once      sync.Once
	ended     chan struct{}
}

func (p *rigProc) Stdin() io.WriteCloser { return p.inW }
func (p *rigProc) Stdout() io.Reader     { return p.outR }
func (p *rigProc) Wait() (int, error)    { return <-p.exitCh, nil }
func (p *rigProc) Kill()                 { p.exit(-1) }

func (p *rigProc) exit(code int) {
	p.once.Do(func() {
		p.outW.Close()
		p.inR.Close()
		close(p.ended)
		p.exitCh <- code
	})
}

func (p *rigProc) say(m trayproto.Msg) {
	m.V = trayproto.Version
	_ = trayproto.Encode(p.outW, m)
}

func (p *rigProc) shell(attached *bool, why string) {
	p.say(trayproto.Msg{Type: trayproto.TypeShell, Attached: attached, Why: why})
}

// quitGot: p was asked to quit (and so ended).
func (p *rigProc) quitGot(t *testing.T) {
	t.Helper()
	select {
	case <-p.ended:
	case <-time.After(5 * time.Second):
		t.Fatalf("helper %q was not asked to quit", p.id)
	}
}

// loopRig is the tray loop of a GNOME session on a fake clock, with a real
// supervisor whose helpers the test plays (rigProc).
type loopRig struct {
	t       *testing.T
	te      *testEnv
	clk     *clock.Fake
	loop    *trayLoop
	icons   chan *rigProc
	bridges chan *rigProc
	events  chan supervisor.Event
	updates chan struct{}
	mu      sync.Mutex
	modes   []string
	opened  []string
	nIcons  int
}

var yes, no = func() *bool { b := true; return &b }(), func() *bool { b := false; return &b }()

func claudeInUse() status.Snapshot {
	return status.Snapshot{Mode: "coordinated", ServerURL: "http://192.0.2.10:8700", Providers: claudeSnap(status.ProviderStatus{
		State: "ok", AccountKey: "acc-0001", MainKey: "weekly_all", Rows: []provider.Row{{Key: "weekly_all", Label: "Weekly", Percent: pf(5)}}})}
}

// newLoopRig starts the loop; prepare runs before (markers).
func newLoopRig(t *testing.T, snap status.Snapshot, prepare func(te *testEnv)) *loopRig {
	t.Helper()
	te := newEnv(t)
	te.GOOS = "linux"
	te.vars["XDG_CURRENT_DESKTOP"] = "ubuntu:GNOME"
	must(t, os.MkdirAll(te.Dirs.Data, 0o700))
	if prepare != nil {
		prepare(te)
	}
	r := &loopRig{t: t, te: te, clk: te.Clock.(*clock.Fake), icons: make(chan *rigProc, 32), bridges: make(chan *rigProc, 32),
		events: make(chan supervisor.Event, 256), updates: make(chan struct{}, 1)}
	te.OpenURL = func(u string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.opened = append(r.opened, u)
		return nil
	}
	sup := supervisor.New(supervisor.Options{Spawn: r.spawn, BridgeEnableOnce: te.Env.panelEnableOnce, Clock: r.clk,
		Log: log.New(io.Discard, "", 0)})
	cfg := mustParse(t, `{"server_urls":["http://192.0.2.10:8700"],"ingest_key":"k-0123456789"}`)
	r.loop = &trayLoop{env: te.Env, ctrl: newController(cfg, snap, nil, nil), sup: sup, updates: r.updates,
		log: log.New(io.Discard, "", 0), remove: func() error { return nil },
		setMode: func(m string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.modes = append(r.modes, m)
		},
		known: []provider.ID{provider.Claude, provider.Codex}, bridge: wantsBridge(te.goos(), te.Getenv),
		handled: func(e supervisor.Event) { r.events <- e }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.loop.run(ctx, "")
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
				r.clk.Advance(time.Second) // a helper that does not quit is killed after 3 s
			}
		}
	})
	return r
}

func (r *loopRig) spawn(p provider.ID, uid uint32, token string) (supervisor.Proc, error) {
	h := &rigProc{id: p, got: make(chan trayproto.Msg, 64), exitCh: make(chan int, 1), ended: make(chan struct{})}
	h.inR, h.inW = io.Pipe()
	h.outR, h.outW = io.Pipe()
	if p != supervisor.BridgeSlot {
		r.mu.Lock()
		r.nIcons++
		r.mu.Unlock()
	}
	go func() {
		br := bufio.NewReader(h.inR)
		first, err := trayproto.Decode(br)
		if err != nil || first.Token != token {
			h.exit(trayproto.ExitRefused)
			return
		}
		h.hello = first
		if p == supervisor.BridgeSlot {
			r.bridges <- h
		} else {
			r.icons <- h
		}
		for {
			m, err := trayproto.Decode(br)
			if err != nil {
				h.exit(0)
				return
			}
			if m.Type == trayproto.TypeQuit {
				h.exit(0)
				return
			}
			select {
			case h.got <- m:
			default:
			}
		}
	}()
	return h, nil
}

func (r *loopRig) icon() *rigProc {
	r.t.Helper()
	select {
	case p := <-r.icons:
		return p
	case <-time.After(5 * time.Second):
		r.t.Fatal("no icon helper started")
		return nil
	}
}

func (r *loopRig) bridge() *rigProc {
	r.t.Helper()
	select {
	case p := <-r.bridges:
		if p.hello.Role != trayproto.RoleShell {
			r.t.Fatalf("bridge hello %+v", p.hello)
		}
		return p
	case <-time.After(5 * time.Second):
		r.t.Fatal("no bridge started")
		return nil
	}
}

// noIcon: no icon helper starts for a while.
func (r *loopRig) noIcon() {
	r.t.Helper()
	select {
	case p := <-r.icons:
		r.t.Fatalf("icon helper %q started", p.id)
	case <-time.After(50 * time.Millisecond):
	}
}

// handled waits until the loop has handled an event that matches.
func (r *loopRig) handled(match func(supervisor.Event) bool) supervisor.Event {
	r.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-r.events:
			if match(e) {
				return e
			}
		case <-deadline:
			r.t.Fatal("the loop did not handle the event")
			return supervisor.Event{}
		}
	}
}

func attachedIs(v bool) func(supervisor.Event) bool {
	return func(e supervisor.Event) bool { return e.Attached != nil && *e.Attached == v }
}

func bridgeExit(e supervisor.Event) bool { return e.Provider == supervisor.BridgeSlot && e.Exit != nil }

func iconExit(e supervisor.Event) bool { return e.Provider != supervisor.BridgeSlot && e.Exit != nil }

// attach: the bridge says attached; the icon helper c (if any) quits.
func (r *loopRig) attach(b, c *rigProc) {
	r.t.Helper()
	b.shell(yes, "")
	r.handled(attachedIs(true))
	if c != nil {
		c.quitGot(r.t)
		r.handled(iconExit)
	}
}

// supported: the bridge says a GNOME Shell the panel supports answered.
func (r *loopRig) supported(b *rigProc) {
	r.t.Helper()
	b.say(trayproto.Msg{Type: trayproto.TypeShell, Supported: yes})
	r.handled(func(e supervisor.Event) bool { return e.Supported != nil && *e.Supported })
}

func (r *loopRig) marker(name string) bool {
	_, err := os.Stat(r.te.Dirs.File(name))
	return err == nil
}

// markerGone waits until the marker is gone.
func (r *loopRig) markerGone(name string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.marker(name) {
		if time.Now().After(deadline) {
			r.t.Fatalf("%s still there", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *loopRig) lastMode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.modes) == 0 {
		return ""
	}
	return r.modes[len(r.modes)-1]
}

func (r *loopRig) iconStarts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nIcons
}

func withMarker(t *testing.T, name string) func(te *testEnv) {
	return func(te *testEnv) { must(t, os.WriteFile(te.Dirs.File(name), nil, 0o600)) }
}

// Without the gnome-panel-attached marker the icon comes at once, beside
// the bridge (design 6.4).
func TestNoMarkerIconsAtOnce(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	_ = r.bridge()
	if c := r.icon(); c.id != provider.Claude {
		t.Fatalf("%q", c.id)
	}
}

// With the marker the icons wait 10 s for the panel; without an attach
// by then they come, and the marker goes when a supported shell answered
// meanwhile (the panel is broken), else it stays (review I1: no shell yet,
// e.g. a bus that is not back after a logout).
func TestMarkerHoldsIconsTenSeconds(t *testing.T) {
	for _, shell := range []bool{true, false} {
		r := newLoopRig(t, claudeInUse(), withMarker(t, paths.PanelAttachedFile))
		b := r.bridge()
		r.noIcon()
		if shell {
			r.supported(b)
		}
		r.clk.Advance(attachWait - time.Millisecond)
		r.noIcon()
		r.clk.Advance(time.Millisecond)
		if c := r.icon(); c.id != provider.Claude {
			t.Fatalf("%q", c.id)
		}
		if shell {
			r.markerGone(paths.PanelAttachedFile)
		} else {
			time.Sleep(20 * time.Millisecond)
			eq(t, r.marker(paths.PanelAttachedFile), true)
		}
	}
	// the marker and an attach within the 10 s: no icon at all
	r2 := newLoopRig(t, claudeInUse(), withMarker(t, paths.PanelAttachedFile))
	b := r2.bridge()
	r2.clk.Advance(5 * time.Second)
	r2.attach(b, nil)
	r2.clk.Advance(time.Hour)
	r2.noIcon()
	eq(t, r2.iconStarts(), 0)
	eq(t, r2.marker(paths.PanelAttachedFile), true)
}

// Attached: the icon goes and the marker is written; a Detach gives the
// panel 10 s to come back, then the icon comes and the marker goes.
func TestDetachGraceThenIcons(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.attach(b, c)
	eq(t, r.marker(paths.PanelAttachedFile), true)
	r.clk.Advance(time.Hour)
	r.noIcon()
	b.shell(no, "detach")
	r.handled(attachedIs(false))
	r.clk.Advance(detachGrace - time.Millisecond)
	r.noIcon()
	r.clk.Advance(time.Millisecond)
	if c := r.icon(); c.id != provider.Claude {
		t.Fatalf("%q", c.id)
	}
	r.noIcon()
	r.markerGone(paths.PanelAttachedFile)
	// the shell's owner gone, or the bridge ended (not 75): the same grace
	for _, end := range []func(b *rigProc){
		func(b *rigProc) { b.shell(no, "owner") },
		func(b *rigProc) { b.exit(2) },
	} {
		r := newLoopRig(t, claudeInUse(), nil)
		b, c := r.bridge(), r.icon()
		r.clk.Advance(time.Minute)
		r.attach(b, c)
		end(b)
		r.handled(func(e supervisor.Event) bool { return attachedIs(false)(e) || bridgeExit(e) })
		r.clk.Advance(detachGrace - time.Millisecond)
		r.noIcon()
		r.clk.Advance(time.Millisecond)
		_ = r.icon()
	}
}

// RF2: a Detach and an attach 6 s later (a shell restart, a lock that the
// bridge did not hold back) start no icon at all.
func TestReattachWithinGraceStartsNothing(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.attach(b, c)
	before := r.iconStarts()
	b.shell(no, "detach")
	r.handled(attachedIs(false))
	r.clk.Advance(6 * time.Second)
	b.shell(yes, "")
	r.handled(attachedIs(true))
	r.clk.Advance(time.Hour)
	r.noIcon()
	eq(t, r.iconStarts(), before)
	eq(t, r.marker(paths.PanelAttachedFile), true)
	// the attach ended that grace: a Detach 3 s after it gets a whole one
	b.shell(no, "detach")
	r.handled(attachedIs(false))
	r.clk.Advance(6 * time.Second)
	b.shell(yes, "")
	r.handled(attachedIs(true))
	r.clk.Advance(3 * time.Second)
	b.shell(no, "detach")
	r.handled(attachedIs(false))
	r.clk.Advance(detachGrace - time.Millisecond)
	r.noIcon()
	r.clk.Advance(time.Millisecond)
	_ = r.icon()
}

// A Ping lapse (the extension died 90 s ago) shows the icon at once.
func TestPingLapseShowsIconsAtOnce(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.attach(b, c)
	b.shell(no, "ping")
	_ = r.icon() // no clock moved
	r.markerGone(paths.PanelAttachedFile)
}

// The bridge stopped after repeated failures shows the icon at once.
func TestStoppedBridgeShowsIconsAtOnce(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.attach(b, c)
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		b.exit(3)
		if e := r.handled(bridgeExit); e.Stopped {
			t.Fatalf("stopped after %d", i+1)
		}
		r.clk.Advance(wait)
		b = r.bridge()
		r.attach(b, nil)
	}
	b.exit(3)
	if e := r.handled(bridgeExit); !e.Stopped {
		t.Fatalf("%+v", e)
	}
	_ = r.icon()
	// no supported shell answers now: the marker stays (review I1)
	eq(t, r.marker(paths.PanelAttachedFile), true)
}

// The bridge found no supported GNOME Shell, or none at all: the icons
// come at once, even while the marker holds them (ruling in the 4b
// report). The marker goes when the shell answered with a version the
// panel does not support (no panel will attach on this desktop), and stays
// for "no shell" and "no shell version" (a session not up yet: review I1;
// ruling of 4b fix round 2).
func TestUnsupportedShellShowsIconsAtOnce(t *testing.T) {
	for why, kept := range map[string]bool{"no shell": true, "no shell version": true, "unsupported 44.2": false} {
		r := newLoopRig(t, claudeInUse(), withMarker(t, paths.PanelAttachedFile))
		b := r.bridge()
		r.noIcon()
		b.say(trayproto.Msg{Type: trayproto.TypeShell, Supported: no, Why: why})
		_ = r.icon()
		if kept {
			time.Sleep(20 * time.Millisecond)
			if !r.marker(paths.PanelAttachedFile) {
				t.Fatalf("%s: the marker went", why)
			}
		} else {
			r.markerGone(paths.PanelAttachedFile)
		}
		b.exit(trayproto.ExitUnsupported)
		r.handled(bridgeExit)
		r.clk.Advance(time.Hour)
		select {
		case p := <-r.bridges:
			t.Fatalf("%s: the bridge was started again: %+v", why, p.hello)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Review H4: the start-up wait with the marker is the one wait a lost bus
// may start: a bridge that ends with 75 during it (it had a bus and lost
// it) does not start a second one, so the icons come 10 s after the start.
func TestStartupWaitIsTheOneWait(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), withMarker(t, paths.PanelAttachedFile))
	b := r.bridge()
	r.clk.Advance(5 * time.Second)
	b.exit(trayproto.ExitSessionLost)
	r.handled(bridgeExit)
	r.clk.Advance(time.Second)
	_ = r.bridge()
	r.clk.Advance(4*time.Second - time.Millisecond)
	r.noIcon()
	r.clk.Advance(time.Millisecond)
	_ = r.icon()
	time.Sleep(20 * time.Millisecond)
	eq(t, r.marker(paths.PanelAttachedFile), true) // no supported shell answered
}

// Review H1: the shell leaving after the extension detached (the user
// turned it off, then logged out; or a lock held the detach, then the
// session ended) is told by the bridge with why "owner": the icons come
// when the grace ends, and the marker stays, as no supported shell
// answers (design 6.4: not removed when a session ends).
func TestShellLeftAfterADetachKeepsTheMarker(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.supported(b)
	r.attach(b, c)
	b.shell(no, "detach")
	r.handled(attachedIs(false))
	r.clk.Advance(3 * time.Second)
	b.shell(no, "owner") // no caller: the bridge tells the shell's leaving all the same
	r.handled(attachedIs(false))
	r.clk.Advance(detachGrace - 3*time.Second - time.Millisecond)
	r.noIcon()
	r.clk.Advance(time.Millisecond)
	_ = r.icon()
	time.Sleep(20 * time.Millisecond)
	eq(t, r.marker(paths.PanelAttachedFile), true)
}

// Review I1: a logout. The shell leaves, then the session bus: the bridge
// ends with 75 and the icons wait 10 s from then. A bridge started again
// waits for its bus in its process (the lost-bus ruling); should its bus
// come and go again before any supported shell answered, its further exits
// with 75 (played here by four quick ones) do not restart the wait. When
// the wait ends the icons come but the marker stays (no supported shell
// answered), so the next login's agent waits for its panel.
func TestLogoutKeepsTheMarker(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.clk.Advance(time.Minute)
	r.supported(b)
	r.attach(b, c)
	b.shell(no, "owner") // the shell went first
	r.handled(attachedIs(false))
	r.clk.Advance(8 * time.Second)
	b.exit(trayproto.ExitSessionLost) // T: the wait runs to T+10 s
	r.handled(bridgeExit)
	// started again at T+1, T+2, T+4 and T+8 (the backoff); each finds no bus
	for _, d := range []time.Duration{time.Second, time.Second, 2 * time.Second, 4 * time.Second} {
		r.clk.Advance(d)
		b = r.bridge()
		b.exit(trayproto.ExitSessionLost)
		r.handled(bridgeExit)
		r.noIcon()
	}
	r.clk.Advance(2*time.Second - time.Millisecond)
	r.noIcon()
	r.clk.Advance(time.Millisecond)
	_ = r.icon()
	time.Sleep(20 * time.Millisecond)
	eq(t, r.marker(paths.PanelAttachedFile), true)
	// the first 75 after an attach does start a new wait, and the new
	// login's panel attaching within it starts no icon
	r2 := newLoopRig(t, claudeInUse(), nil)
	b, c = r2.bridge(), r2.icon()
	r2.clk.Advance(time.Minute)
	r2.attach(b, c)
	before := r2.iconStarts()
	b.exit(trayproto.ExitSessionLost)
	r2.handled(bridgeExit)
	r2.clk.Advance(time.Second)
	b = r2.bridge()
	r2.clk.Advance(5 * time.Second)
	r2.supported(b)
	b.shell(yes, "")
	r2.handled(attachedIs(true))
	r2.clk.Advance(time.Hour)
	r2.noIcon()
	eq(t, r2.iconStarts(), before)
	eq(t, r2.marker(paths.PanelAttachedFile), true)
}

// Review I1: the shell left and its bus outlives the grace (a slow
// logout): the icons come after the grace, the marker stays, as no shell
// answers; a shell that comes back and attaches is the panel again.
func TestShellGoneKeepsTheMarker(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.supported(b)
	r.attach(b, c)
	b.shell(no, "owner")
	r.handled(attachedIs(false))
	r.clk.Advance(detachGrace)
	c = r.icon()
	time.Sleep(20 * time.Millisecond)
	eq(t, r.marker(paths.PanelAttachedFile), true)
	r.attach(b, c)
	b.shell(no, "detach") // a shell there: its Detach ends the panel
	r.handled(attachedIs(false))
	r.clk.Advance(detachGrace)
	_ = r.icon()
	r.markerGone(paths.PanelAttachedFile)
}

// Review m6: a second end while a grace runs keeps its deadline (the
// shell's owner left, then the bridge ended): the icons come 10 s after
// the first.
func TestGraceKeepsItsDeadline(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.clk.Advance(time.Minute)
	r.attach(b, c)
	b.shell(no, "owner")
	r.handled(attachedIs(false))
	r.clk.Advance(5 * time.Second)
	b.exit(2)
	r.handled(bridgeExit)
	r.clk.Advance(5*time.Second - time.Millisecond)
	r.noIcon()
	r.clk.Advance(time.Millisecond)
	_ = r.icon()
}

// enabled:true writes gnome-panel-enabled once (design 6.6).
func TestEnabledWritesMarkerOnce(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b := r.bridge()
	eq(t, r.marker(paths.PanelEnabledFile), false)
	b.say(trayproto.Msg{Type: trayproto.TypeShell, Enabled: true})
	r.handled(func(e supervisor.Event) bool { return e.Enabled })
	eq(t, r.marker(paths.PanelEnabledFile), true)
	p := r.te.Dirs.File(paths.PanelEnabledFile)
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	must(t, os.Chtimes(p, old, old))
	b.say(trayproto.Msg{Type: trayproto.TypeShell, Enabled: true})
	r.handled(func(e supervisor.Event) bool { return e.Enabled })
	st, err := os.Stat(p)
	must(t, err)
	if !st.ModTime().Equal(old) {
		t.Fatalf("rewritten at %v", st.ModTime())
	}
}

// The bridge's enable_once follows the gnome-panel-enabled marker at each
// of its starts (ruling F15): no marker, true; the marker, false; and
// after enabled:true a restarted bridge (the next login) carries false.
func TestHelloEnableOnceFollowsMarker(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), withMarker(t, paths.PanelEnabledFile))
	eq(t, r.bridge().hello.EnableOnce, false)
	r = newLoopRig(t, claudeInUse(), nil)
	b := r.bridge()
	eq(t, b.hello.EnableOnce, true)
	b.say(trayproto.Msg{Type: trayproto.TypeShell, Enabled: true})
	r.handled(func(e supervisor.Event) bool { return e.Enabled })
	r.clk.Advance(time.Minute)
	b.exit(trayproto.ExitSessionLost)
	r.handled(bridgeExit)
	r.clk.Advance(time.Second)
	eq(t, r.bridge().hello.EnableOnce, false)
}

// client.mode is "tray" while the panel is attached, though no icon is up.
func TestClientModeTrayWhenAttached(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), nil)
	b, c := r.bridge(), r.icon()
	r.attach(b, c)
	eq(t, r.lastMode(), "tray")
	b.shell(no, "ping")
	r.handled(attachedIs(false))
	_ = r.icon() // its icon is not up yet
	eq(t, r.lastMode(), "headless")
}

// RF4: a click from the panel on a row the snapshot no longer has opens
// nothing; one on a row it has opens that account's page.
func TestClickOnVanishedRowOpensNothing(t *testing.T) {
	rows := func(n int) status.ProviderStatus {
		ps := status.ProviderStatus{State: "ok", AccountKey: "acc-0002", MainKey: "primary"}
		for i := range n {
			ps.Rows = append(ps.Rows, provider.Row{Key: "k" + string(rune('a'+i)), Label: "L", Percent: pf(10)})
		}
		return ps
	}
	snap := claudeInUse()
	snap.Providers[provider.Codex] = rows(4)
	r := newLoopRig(t, snap, nil)
	b := r.bridge()
	snap.Providers = map[provider.ID]status.ProviderStatus{provider.Claude: snap.Providers[provider.Claude], provider.Codex: rows(3)}
	r.loop.ctrl.set(snap)
	b.say(trayproto.Msg{Type: trayproto.TypeClick, ID: "r.codex.3"})
	r.handled(func(e supervisor.Event) bool { return e.Click == "r.codex.3" })
	b.say(trayproto.Msg{Type: trayproto.TypeClick, ID: "r.codex.2"})
	r.handled(func(e supervisor.Event) bool { return e.Click == "r.codex.2" })
	r.mu.Lock()
	defer r.mu.Unlock()
	eq(t, r.opened, []string{"http://192.0.2.10:8700/accounts/acc-0002"})
}

// The bridge runs in a GNOME session on Linux only, and only once the
// helper can serve it (bridgeReady).
func TestNoBridgeOutsideGNOME(t *testing.T) {
	for _, c := range []struct {
		goos, desktop string
		want          bool
	}{
		{"linux", "GNOME", true}, {"linux", "ubuntu:GNOME", true}, {"linux", "gnome-classic:GNOME", true},
		{"linux", "KDE", false}, {"linux", "", false}, {"linux", "XFCE", false},
		{"windows", "GNOME", false}, {"darwin", "GNOME", false},
	} {
		getenv := func(k string) string {
			if k == "XDG_CURRENT_DESKTOP" {
				return c.desktop
			}
			return ""
		}
		if got := wantsBridge(c.goos, getenv); got != c.want {
			t.Errorf("%s %q: %v", c.goos, c.desktop, got)
		}
	}
	// a run: the bridge only when the helper can serve it, beside the icon
	for _, c := range []struct {
		ready   bool
		desktop string
		bridge  bool
	}{{false, "ubuntu:GNOME", false}, {true, "ubuntu:GNOME", true}, {true, "KDE", false}} {
		ids := func() []provider.ID {
			old := bridgeReady
			bridgeReady = c.ready
			defer func() { bridgeReady = old }() // review m7: also when the run fails
			fs := newFakeServer(t)
			te := trayEnv(t, fs)
			te.GOOS = "linux"
			te.vars["XDG_SESSION_TYPE"] = "wayland"
			te.vars["XDG_CURRENT_DESKTOP"] = c.desktop
			cancel, code := te.mainAsync("run")
			_ = te.helpers.ready()
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) && te.helpers.count() < 2 {
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			eq(t, waitCode(t, code), 0)
			te.helpers.mu.Lock()
			defer te.helpers.mu.Unlock()
			var ids []provider.ID
			for _, h := range te.helpers.started {
				ids = append(ids, h.id)
			}
			return ids
		}()
		if got := slices.Contains(ids, supervisor.BridgeSlot); got != c.bridge || len(ids) != map[bool]int{false: 1, true: 2}[c.bridge] {
			t.Fatalf("ready %v desktop %q: helpers %q", c.ready, c.desktop, ids)
		}
	}
}

// A marker left from a session before is not read where no bridge runs.
func TestMarkerIgnoredWithoutBridge(t *testing.T) {
	r := newLoopRig(t, claudeInUse(), func(te *testEnv) {
		te.vars["XDG_CURRENT_DESKTOP"] = "KDE"
		must(t, os.WriteFile(te.Dirs.File(paths.PanelAttachedFile), nil, 0o600))
	})
	_ = r.icon()
	select {
	case <-r.bridges:
		t.Fatal("a bridge outside GNOME")
	default:
	}
}
