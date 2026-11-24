package trayhelper

import (
	"bufio"
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// fakeUI stands in for the tray library: Run calls onReady and blocks
// until Quit, then calls onExit, as the library does.
type fakeUI struct {
	mu       sync.Mutex
	ran      chan struct{}
	quit     chan struct{}
	quitOnce sync.Once
	ranOnce  sync.Once
	onExit   func()
	noReady  bool // Run never calls onReady (a tray that cannot start)
	// readyReturned is closed when onReady returned
	readyReturned chan struct{}

	layout   []string // item ids and "-" for separators, as created
	items    map[string]*fakeItem
	resets   int
	icons    [][]byte
	titles   []string
	tooltips []string
	lost     chan struct{}
	shown    atomic.Bool
	size     atomic.Int32
}

type fakeItem struct {
	mu                        sync.Mutex
	id                        string
	checkbox                  bool
	title                     string
	titles                    int
	enabled, visible, checked bool
	image                     []byte
	clicked                   chan struct{}
}

func newFakeUI() *fakeUI {
	u := &fakeUI{ran: make(chan struct{}), quit: make(chan struct{}), items: map[string]*fakeItem{}, lost: make(chan struct{}), readyReturned: make(chan struct{})}
	u.shown.Store(true)
	return u
}

func (u *fakeUI) Run(onReady, onExit func()) {
	u.mu.Lock()
	u.onExit = onExit
	u.mu.Unlock()
	u.ranOnce.Do(func() { close(u.ran) })
	if !u.noReady {
		go func() {
			// the library runs onReady on a goroutine of its own, and
			// answers the tray host's first request for the menu only once
			// onReady has returned (initialMenuBuilt)
			onReady()
			close(u.readyReturned)
		}()
	}
	<-u.quit
	onExit()
}
func (u *fakeUI) Quit() { u.quitOnce.Do(func() { close(u.quit) }) }

// endSession is what Windows' WM_ENDSESSION does: onExit without Quit.
func (u *fakeUI) endSession() {
	u.mu.Lock()
	f := u.onExit
	u.mu.Unlock()
	f()
}

func (u *fakeUI) AddItem(id string, checkbox bool) uiItem {
	u.mu.Lock()
	defer u.mu.Unlock()
	it := &fakeItem{id: id, checkbox: checkbox, clicked: make(chan struct{})}
	u.layout = append(u.layout, id)
	u.items[id] = it
	return it
}
func (u *fakeUI) AddSeparator() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.layout = append(u.layout, "-")
}
func (u *fakeUI) Reset() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, it := range u.items {
		close(it.clicked)
	}
	u.layout, u.items = nil, map[string]*fakeItem{}
	u.resets++
}
func (u *fakeUI) SetIcon(img []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.icons = append(u.icons, img)
}
func (u *fakeUI) SetNextToIcon(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.titles = append(u.titles, s)
}
func (u *fakeUI) SetTooltip(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tooltips = append(u.tooltips, s)
}
func (u *fakeUI) Lost() <-chan struct{} { return u.lost }
func (u *fakeUI) Shown() bool           { return u.shown.Load() }
func (u *fakeUI) IconSize() int         { return int(u.size.Load()) }

func (u *fakeUI) snapshot() (layout []string, items map[string]fakeItem, icons, titles, tooltips int, resets int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	items = map[string]fakeItem{}
	for k, v := range u.items {
		v.mu.Lock()
		items[k] = fakeItem{id: v.id, checkbox: v.checkbox, title: v.title, titles: v.titles, enabled: v.enabled, visible: v.visible, checked: v.checked, image: v.image}
		v.mu.Unlock()
	}
	return append([]string(nil), u.layout...), items, len(u.icons), len(u.titles), len(u.tooltips), u.resets
}

func (i *fakeItem) SetTitle(s string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.title = s
	i.titles++
}
func (i *fakeItem) SetEnabled(b bool) { i.mu.Lock(); i.enabled = b; i.mu.Unlock() }
func (i *fakeItem) SetVisible(b bool) { i.mu.Lock(); i.visible = b; i.mu.Unlock() }
func (i *fakeItem) SetChecked(b bool) { i.mu.Lock(); i.checked = b; i.mu.Unlock() }
func (i *fakeItem) SetImage(b []byte) { i.mu.Lock(); i.image = b; i.mu.Unlock() }
func (i *fakeItem) Clicked() <-chan struct{} {
	return i.clicked
}

// run is one helper run with pipes for its standard input and output.
type run struct {
	t       *testing.T
	ui      *fakeUI
	iconDir string
	opts    chan Options // what the tray was started for
	inW     *io.PipeWriter
	out     *bufio.Reader
	code    chan int
	notices chan string
	gaveUp  atomic.Bool
}

type runOpts struct {
	goos      string
	token     string
	available func(context.Context) (bool, string)
	waitHost  func(context.Context) bool
	watch     func(context.Context) bool
	ui        *fakeUI
}

func startRun(t *testing.T, ro runOpts) *run {
	t.Helper()
	if ro.goos == "" {
		ro.goos = "linux"
	}
	if ro.ui == nil {
		ro.ui = newFakeUI()
	}
	if ro.available == nil {
		ro.available = func(context.Context) (bool, string) { return true, "" }
	}
	if ro.waitHost == nil {
		ro.waitHost = func(ctx context.Context) bool { <-ctx.Done(); return false }
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &run{t: t, ui: ro.ui, iconDir: t.TempDir(), opts: make(chan Options, 1), inW: inW, out: bufio.NewReader(outR), code: make(chan int, 1), notices: make(chan string, 4)}
	d := deps{
		goos: ro.goos, getenv: func(k string) string { return map[string]string{"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}[k] },
		newUI: func(o Options) ui { r.opts <- o; return ro.ui }, available: ro.available, waitHost: ro.waitHost, watch: ro.watch,
		notify:       func(title, text string) error { r.notices <- title + ": " + text; return nil },
		tick:         20 * time.Millisecond,
		readyTimeout: 2 * time.Second,
		giveUp:       func() { r.gaveUp.Store(true); ro.ui.Quit() },
	}
	go func() {
		r.code <- runWith(context.Background(), inR, outW, "tok-0123456789", d)
		outW.Close()
		inR.Close()
	}()
	t.Cleanup(func() { inW.Close() })
	return r
}

func (r *run) send(m trayproto.Msg) {
	r.t.Helper()
	m.V = trayproto.Version
	if err := trayproto.Encode(r.inW, m); err != nil {
		r.t.Fatal(err)
	}
}

func (r *run) hello() {
	r.send(trayproto.Msg{Type: trayproto.TypeHello, Token: "tok-0123456789", Provider: "claude", UID: 100, IconDir: r.iconDir})
}

// next is the helper's next line.
func (r *run) next() trayproto.Msg {
	r.t.Helper()
	ch := make(chan trayproto.Msg, 1)
	go func() {
		m, err := trayproto.Decode(r.out)
		if err != nil {
			m = trayproto.Msg{Type: "error: " + err.Error()}
		}
		ch <- m
	}()
	select {
	case m := <-ch:
		return m
	case <-time.After(5 * time.Second):
		r.t.Fatal("the helper said nothing")
		return trayproto.Msg{}
	}
}

func (r *run) exitCode() int {
	r.t.Helper()
	select {
	case c := <-r.code:
		return c
	case <-time.After(5 * time.Second):
		r.t.Fatal("the helper did not end")
		return -1
	}
}

// view is a small menu: Claude's section, the server line and Quit.
func view(title string, codex bool) trayproto.Msg {
	items := make([]trayproto.Item, 0, len(trayproto.ItemIDs))
	for _, id := range trayproto.ItemIDs {
		it := trayproto.Item{ID: id}
		switch {
		case id == "h.claude":
			it.Text, it.Visible, it.Image = "Claude", true, "claude"
		case id == "r.claude.0":
			it.Text, it.Visible, it.Enabled = "Weekly "+title, true, true
		case id == "h.codex" && codex:
			it.Text, it.Visible = "Codex", true
		case id == "server":
			it.Text, it.Visible = "Server connected", true
		case id == "autostart":
			it.Text, it.Visible, it.Enabled, it.Checkbox, it.Checked = "Start at login", true, true, true, true
		case id == "quit":
			it.Text, it.Visible, it.Enabled = "Quit", true, true
		}
		items = append(items, it)
	}
	p := 42.0
	return trayproto.Msg{Type: trayproto.TypeView, Icon: &trayproto.Icon{Provider: "claude", Percent: &p, Level: "normal"},
		Title: title, Tooltip: "AI Usage · Claude\nWeekly " + title, Items: items}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHelperRefusesWithoutToken(t *testing.T) {
	for _, first := range []trayproto.Msg{
		{Type: trayproto.TypeHello, Token: "wrong"},
		{Type: trayproto.TypeHello},
		{Type: trayproto.TypeView},
	} {
		r := startRun(t, runOpts{})
		r.send(first)
		eq(t, r.exitCode(), trayproto.ExitRefused)
		select {
		case <-r.ui.ran:
			t.Fatal("the tray was started")
		default:
		}
	}
	// nothing at all before the end of standard input
	r := startRun(t, runOpts{})
	r.inW.Close()
	eq(t, r.exitCode(), trayproto.ExitRefused)
}

// What the helper is for comes in its first line (stage-2 review m-4):
// its command line names nothing.
func TestHelperTakesItsJobFromTheFirstLine(t *testing.T) {
	r := startRun(t, runOpts{goos: "windows"})
	dir := t.TempDir()
	r.send(trayproto.Msg{Type: trayproto.TypeHello, Token: "tok-0123456789", Provider: "codex", UID: 101, IconDir: dir})
	select {
	case o := <-r.opts:
		eq(t, o, Options{Provider: "codex", UID: 101, IconDir: dir})
	case <-time.After(5 * time.Second):
		t.Fatal("the tray was not started")
	}
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	r.send(trayproto.Msg{Type: trayproto.TypeQuit})
	eq(t, r.exitCode(), trayproto.ExitOK)
}

// In standby (the agent stopped this helper after repeated failures) it
// shows nothing and only watches the desktop: when the tray came (back) it
// ends with 77, and the agent tries the icon again (stage-2 review m-3).
func TestHelperStandbyWatchesTheDesktop(t *testing.T) {
	back := make(chan struct{})
	r := startRun(t, runOpts{watch: func(ctx context.Context) bool {
		select {
		case <-back:
			return true
		case <-ctx.Done():
			return false
		}
	}})
	r.send(trayproto.Msg{Type: trayproto.TypeHello, Token: "tok-0123456789", Provider: "claude", UID: 100, Standby: true})
	r.send(view("42%", false)) // not shown
	select {
	case <-r.code:
		t.Fatal("ended before the desktop came back")
	case <-time.After(50 * time.Millisecond):
	}
	close(back)
	eq(t, r.exitCode(), trayproto.ExitDesktopBack)
	select {
	case <-r.ui.ran:
		t.Fatal("the tray was started")
	case o := <-r.opts:
		t.Fatalf("a tray was made for %+v", o)
	default:
	}
}

// A standby helper ends on quit and at the end of its input, like any.
func TestHelperStandbyEndsOnQuitAndEOF(t *testing.T) {
	wait := func(ctx context.Context) bool { <-ctx.Done(); return false }
	for _, end := range []string{"quit", "eof"} {
		r := startRun(t, runOpts{watch: wait})
		r.send(trayproto.Msg{Type: trayproto.TypeHello, Token: "tok-0123456789", Standby: true})
		if end == "quit" {
			r.send(trayproto.Msg{Type: trayproto.TypeQuit})
		} else {
			r.inW.Close()
		}
		eq(t, r.exitCode(), trayproto.ExitOK)
	}
	// where nothing can be watched (macOS) it ends at once
	r := startRun(t, runOpts{})
	r.send(trayproto.Msg{Type: trayproto.TypeHello, Token: "tok-0123456789", Standby: true})
	eq(t, r.exitCode(), trayproto.ExitOK)
}

func TestHelperExitsOnStdinEOF(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	r.inW.Close() // the agent went away
	eq(t, r.exitCode(), trayproto.ExitOK)
	select {
	case <-r.ui.quit:
	default:
		t.Fatal("Quit was not called")
	}
}

func TestHelperQuits(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	r.send(trayproto.Msg{Type: trayproto.TypeQuit})
	eq(t, r.exitCode(), trayproto.ExitOK)
}

func TestHelperSessionLostExits75(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	close(r.ui.lost)
	eq(t, r.exitCode(), trayproto.ExitSessionLost)
}

func TestHelperSessionEndExits76(t *testing.T) {
	r := startRun(t, runOpts{goos: "windows"})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	r.ui.endSession()
	eq(t, r.exitCode(), trayproto.ExitSessionEnded)
}

func TestHelperWaitsForTheTrayHost(t *testing.T) {
	appeared := make(chan struct{})
	r := startRun(t, runOpts{
		available: func(context.Context) (bool, string) { return false, "no StatusNotifierWatcher" },
		waitHost: func(ctx context.Context) bool {
			select {
			case <-appeared:
				return true
			case <-ctx.Done():
				return false
			}
		},
	})
	r.hello()
	r.send(view("42%", false))
	m := r.next()
	eq(t, m.Type, trayproto.TypeWaiting)
	eq(t, m.Why, "no StatusNotifierWatcher")
	r.send(view("43%", false)) // views while waiting: the newest is shown
	select {
	case <-r.ui.ran:
		t.Fatal("the tray started without a host")
	case <-time.After(50 * time.Millisecond):
	}
	close(appeared)
	eq(t, r.next().Type, trayproto.TypeReady)
	waitFor(t, "the newest view", func() bool {
		_, items, _, _, _, _ := r.ui.snapshot()
		return items["r.claude.0"].title == "Weekly 43%"
	})
}

func TestHelperEndsWhileWaitingForTheHost(t *testing.T) {
	r := startRun(t, runOpts{available: func(context.Context) (bool, string) { return false, "no session bus" }})
	r.hello()
	eq(t, r.next().Why, "no session bus")
	r.inW.Close()
	eq(t, r.exitCode(), trayproto.ExitOK)
	select {
	case <-r.ui.ran:
		t.Fatal("the tray was started")
	default:
	}
}

func TestHelperMenuOnlyChanges(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, items, icons, titles, tips, resets := r.ui.snapshot()
	want := []string{"newversion", "h.claude", "r.claude.0", "r.claude.1", "r.claude.2", "r.claude.3", "r.claude.4", "p.claude", "-",
		"server", "-", "dashboard", "refresh", "autostart", "remove", "quit"}
	eq(t, layout, want)
	eq(t, items["autostart"].checkbox, true)
	eq(t, items["autostart"].checked, true)
	eq(t, items["r.claude.0"].enabled, true)
	eq(t, items["h.claude"].enabled, false)
	eq(t, items["r.claude.1"].visible, false)
	eq(t, items["quit"].title, "Quit")
	// the provider's header has its mascot picture (Clawd)
	eq(t, string(items["h.claude"].image), string(icon.MenuImage("linux", icon.Claude)))
	eq(t, items["quit"].image == nil, true)
	eq(t, [3]int{icons, titles, tips}, [3]int{1, 1, 1})
	before := items["quit"].titles

	r.send(view("42%", false)) // the same again: nothing is set
	r.send(view("57%", false)) // one row and the icon's text change
	waitFor(t, "the change", func() bool {
		_, items, _, _, tips, _ := r.ui.snapshot()
		return items["r.claude.0"].title == "Weekly 57%" && tips == 2
	})
	_, items, icons, titles, tips, resets2 := r.ui.snapshot()
	eq(t, items["quit"].titles, before)
	eq(t, [3]int{icons, titles, tips}, [3]int{1, 2, 2}) // same picture: not set again
	eq(t, resets2, resets)
}

func TestHelperMenuGetsASectionWhenAProviderAppears(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	r.send(view("42%", true))
	waitFor(t, "the Codex section", func() bool {
		layout, items, _, _, _, _ := r.ui.snapshot()
		return strings.Contains(strings.Join(layout, " "), "h.codex") && layout[len(layout)-1] == "quit" && items["quit"].visible
	})
	layout, items, _, _, _, resets := r.ui.snapshot()
	eq(t, resets, 1)
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude - "+
		"h.codex r.codex.0 r.codex.1 r.codex.2 r.codex.3 r.codex.4 p.codex - server - dashboard refresh autostart remove quit")
	eq(t, items["r.claude.0"].title, "Weekly 42%") // everything is set again after the reset
	eq(t, items["autostart"].checked, true)
}

func TestHelperSendsClicks(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	r.ui.mu.Lock()
	ch := r.ui.items["r.claude.0"].clicked
	r.ui.mu.Unlock()
	ch <- struct{}{}
	m := r.next()
	eq(t, m.Type, trayproto.TypeClick)
	eq(t, m.ID, "r.claude.0")
}

// Windows: an icon the notification area did not take (the taskbar was
// not there yet) is "waiting" until it is added; the menu works anyway.
func TestHelperWaitsForTheTaskbar(t *testing.T) {
	u := newFakeUI()
	u.shown.Store(false)
	r := startRun(t, runOpts{goos: "windows", ui: u})
	r.hello()
	r.send(view("42%", false))
	m := r.next()
	eq(t, m.Type, trayproto.TypeWaiting)
	eq(t, m.Why, "no taskbar")
	u.shown.Store(true)
	eq(t, r.next().Type, trayproto.TypeReady)
	u.shown.Store(false) // Explorer restarted and the icon could not be added again
	eq(t, r.next().Why, "no taskbar")
	u.shown.Store(true)
	eq(t, r.next().Type, trayproto.TypeReady)
}

func TestHelperShowsTheNoticeWhenItsIconIsUp(t *testing.T) {
	u := newFakeUI()
	u.shown.Store(false)
	r := startRun(t, runOpts{goos: "windows", ui: u})
	r.hello()
	r.send(trayproto.Msg{Type: trayproto.TypeNotice, Text: "AI Usage is running."})
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeWaiting)
	select {
	case n := <-r.notices:
		t.Fatalf("notice before the icon: %s", n)
	case <-time.After(50 * time.Millisecond):
	}
	u.shown.Store(true)
	eq(t, r.next().Type, trayproto.TypeReady)
	select {
	case n := <-r.notices:
		eq(t, n, "AI Usage: AI Usage is running.")
	case <-time.After(5 * time.Second):
		t.Fatal("no notice")
	}
}

func TestHelperSetsTheIconAgainWhenItsSizeChanges(t *testing.T) {
	u := newFakeUI()
	u.size.Store(16)
	r := startRun(t, runOpts{goos: "windows", ui: u})
	r.hello()
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
	u.size.Store(24)
	waitFor(t, "the icon set again", func() bool {
		_, _, icons, _, _, _ := u.snapshot()
		return icons == 2
	})
}

func TestHelperGivesUpWhenTheTrayNeverStarts(t *testing.T) {
	u := newFakeUI()
	u.noReady = true
	r := startRun(t, runOpts{ui: u})
	r.hello()
	r.send(view("42%", false))
	r.exitCode()
	if !r.gaveUp.Load() {
		t.Fatal("did not give up")
	}
}

// onReady returns once the menu is built: the Linux library answers the
// tray host's GetLayout only after that (a loop run inside onReady left
// the menu unreadable).
func TestHelperOnReadyReturnsWithTheMenuBuilt(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	r.send(view("42%", false))
	select {
	case <-r.ui.readyReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("onReady did not return")
	}
	layout, items, _, _, _, _ := r.ui.snapshot()
	if len(layout) == 0 || items["r.claude.0"].title != "Weekly 42%" {
		t.Fatalf("onReady returned before the menu was built: %v", layout)
	}
	eq(t, r.next().Type, trayproto.TypeReady)
	r.send(view("43%", false)) // and the helper goes on working
	waitFor(t, "the next view", func() bool {
		_, items, _, _, _, _ := r.ui.snapshot()
		return items["r.claude.0"].title == "Weekly 43%"
	})
}

// A helper that gets no view at first still returns from onReady (the
// menu is built when the view comes).
func TestHelperOnReadyReturnsWithoutAView(t *testing.T) {
	r := startRun(t, runOpts{})
	r.hello()
	select {
	case <-r.ui.readyReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("onReady did not return")
	}
	r.send(view("42%", false))
	eq(t, r.next().Type, trayproto.TypeReady)
}
