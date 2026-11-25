// Package trayhelper is the tray helper process (`ai-usage tray-helper`,
// design D3, 6.4): one tray icon and its menu, drawn from what the running
// agent sends on standard input, with clicks sent back on standard output
// (package trayproto). It reads no file, uses no network and never sees a
// sign-in: it only draws. Its session's troubles end only the helper: a
// lost session bus ends it with exit code 75, the end of a Windows session
// with 76, and the agent starts it again.
package trayhelper

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/notify"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Options are what a helper is for, from its first line (not its command
// line, which other users of the PC can read).
type Options struct {
	Provider string // "claude", "codex", or "" for the neutral helper
	UID      uint32 // the Windows notification icon id
	IconDir  string // Windows: where its icon files go (in the data folder)
}

// ExitNoTray: this build has no tray (macOS without cgo).
const ExitNoTray = 3

// Run shows one tray icon and blocks until quit, the end of standard
// input, or the loss or end of the session; it returns the exit code. Its
// first line must carry token (from trayproto.EnvToken). It must run on
// the main goroutine (macOS draws there).
func Run(ctx context.Context, in io.Reader, out io.Writer, token string) int {
	return runWith(ctx, in, out, token, deps{
		goos:         runtime.GOOS,
		getenv:       os.Getenv,
		newUI:        newUI,
		watch:        watchSession,
		available:    func(ctx context.Context) (bool, string) { return Available(ctx, os.Getenv) },
		waitHost:     func(ctx context.Context) bool { return WaitForHost(ctx, 0) },
		notify:       notify.Show,
		removeStale:  removeStaleIcons,
		window:       iconWindow,
		tick:         2 * time.Second,
		readyTimeout: 60 * time.Second,
		giveUp:       func() { log.Print("the tray did not start within a minute; ending"); os.Exit(1) },
		bridge:       runBridge,
	})
}

// deps are what a helper run depends on; tests replace them.
type deps struct {
	goos   string
	getenv func(string) string
	// newUI makes the tray for what the first line said (nil: no tray in
	// this build).
	newUI func(Options) ui
	ui    ui
	// watch waits for the desktop's tray to come (back), for a helper in
	// standby (nil: nothing to watch on this OS).
	watch     func(ctx context.Context) bool
	available func(ctx context.Context) (bool, string)
	waitHost  func(ctx context.Context) bool
	notify    func(title, text string) error
	// removeStale removes Windows notification icons that helpers which
	// crashed or were killed left behind (nil: nothing to do on this OS);
	// window is the window of this helper's icon (Windows; nil: none)
	removeStale func([]trayproto.StaleIcon)
	window      func() uint64
	// tick: how often the icon's state is checked (Windows: still in the
	// notification area, its size).
	tick time.Duration
	// readyTimeout: the tray library must have started by then, or giveUp.
	readyTimeout time.Duration
	giveUp       func()
	// bridge serves the usage panel to the GNOME Shell extension, for a
	// hello whose role is "shell" (shellbridge.Run)
	bridge func(ctx context.Context, in *bufio.Reader, out io.Writer, enableOnce bool) int
}

type helper struct {
	d    deps
	o    Options
	out  io.Writer
	omu  sync.Mutex
	msgs chan trayproto.Msg // closed at the end of standard input

	view   *trayproto.Msg
	notice string

	quitting atomic.Bool
	lost     atomic.Bool
	ended    chan struct{}
	endOnce  sync.Once

	// what is shown
	shape      []string              // the sections the menu was built with
	drawing    bool                  // the menu is the drawn usage panel
	drawn      map[string]panel.Item // what each drawn item was last given
	items      map[string]uiItem
	shown      map[string]trayproto.Item
	clicks     chan string
	lastIcon   []byte
	lastSize   int
	lastTitle  *string
	lastTip    *string
	ready      bool
	waitingFor string
}

func runWith(ctx context.Context, in io.Reader, out io.Writer, token string, d deps) int {
	br := bufio.NewReader(in)
	first, err := trayproto.Decode(br)
	if err != nil || first.Type != trayproto.TypeHello || first.V != trayproto.Version || token == "" || first.Token != token {
		log.Print("tray-helper: this command is started by the agent itself")
		return trayproto.ExitRefused
	}
	if len(first.Stale) > 0 && d.removeStale != nil {
		// first: an icon of a crashed helper stays until the pointer
		// passes over it (review n-2)
		d.removeStale(first.Stale)
	}
	switch first.Role {
	case "":
	case trayproto.RoleShell:
		return d.bridge(ctx, br, out, first.EnableOnce)
	default:
		log.Printf("tray-helper: unknown role %q", first.Role)
		return trayproto.ExitRefused
	}
	if first.Standby {
		return standby(ctx, br, d)
	}
	o := Options{Provider: first.Provider, UID: first.UID, IconDir: first.IconDir}
	if d.goos == "windows" {
		log.Printf("notification icon id %d", o.UID)
	}
	d.ui = d.newUI(o)
	if d.ui == nil {
		log.Print("tray-helper: this build has no tray")
		return ExitNoTray
	}
	h := &helper{d: d, o: o, out: out, msgs: make(chan trayproto.Msg, 16), ended: make(chan struct{}), clicks: make(chan string, 16)}
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(h.msgs)
		for {
			m, err := trayproto.Decode(br)
			if err != nil {
				return
			}
			select {
			case h.msgs <- m:
			case <-done:
				return
			}
		}
	}()
	if !h.waitForHost(ctx) {
		return trayproto.ExitOK
	}
	started := make(chan struct{})
	go func() {
		select {
		case <-started:
		case <-done:
		case <-time.After(d.readyTimeout):
			d.giveUp()
		}
	}()
	d.ui.Run(func() {
		close(started)
		// The loop runs on its own: the Linux library answers the tray
		// host's request for the menu only once onReady has returned
		// (initialMenuBuilt). onReady waits for the menu to be built from
		// the first view, a short while at most.
		built := make(chan struct{})
		go h.loop(ctx, built)
		select {
		case <-built:
		case <-time.After(menuWait):
		}
	}, h.onExit)
	if d.goos == "windows" {
		removeIconFiles(o.IconDir)
	}
	switch {
	case h.lost.Load():
		return trayproto.ExitSessionLost
	case !h.quitting.Load():
		select {
		case <-h.ended:
			return trayproto.ExitSessionEnded
		default:
		}
	}
	return trayproto.ExitOK
}

// standby watches the desktop while the agent has stopped this provider's
// icon after repeated failures (design 6.4): it shows nothing, and ends
// with ExitDesktopBack when the desktop's tray came (back), or with ExitOK
// on quit or at the end of its input.
func standby(ctx context.Context, br *bufio.Reader, d deps) int {
	if d.watch == nil {
		log.Print("tray-helper: nothing to watch on this OS")
		return trayproto.ExitOK
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		for {
			m, err := trayproto.Decode(br)
			if err != nil || m.Type == trayproto.TypeQuit {
				return
			}
		}
	}()
	if d.watch(wctx) {
		log.Print("the desktop's tray came back")
		return trayproto.ExitDesktopBack
	}
	return trayproto.ExitOK
}

func (h *helper) send(m trayproto.Msg) {
	m.V = trayproto.Version
	h.omu.Lock()
	defer h.omu.Unlock()
	_ = trayproto.Encode(h.out, m) // a closed output: the agent is gone, standard input ends too
}

// take handles a message that arrived before the tray is up; false when
// the helper is to end.
func (h *helper) take(m trayproto.Msg, ok bool) bool {
	if !ok || m.Type == trayproto.TypeQuit {
		return false
	}
	switch m.Type {
	case trayproto.TypeView:
		h.view = &m
	case trayproto.TypeNotice:
		h.notice = m.Text
	}
	return true
}

// waitForHost waits until the desktop has a tray host (Linux; at once
// elsewhere), saying "waiting" meanwhile. False: the helper is to end.
func (h *helper) waitForHost(ctx context.Context) bool {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		ok  bool
		why string
	}
	avail := make(chan answer, 1)
	go func() {
		ok, why := h.d.available(wctx)
		avail <- answer{ok, why}
	}()
	var found chan bool
	for {
		select {
		case a := <-avail:
			if a.ok {
				return true
			}
			if wctx.Err() != nil {
				return false
			}
			log.Printf("no tray host (%s): waiting for one", a.why)
			h.send(trayproto.Msg{Type: trayproto.TypeWaiting, Why: a.why})
			found = make(chan bool, 1)
			go func() { found <- h.d.waitHost(wctx) }()
		case f := <-found:
			if f {
				log.Print("tray host found")
			}
			return f
		case m, ok := <-h.msgs:
			if !h.take(m, ok) {
				return false
			}
		case <-ctx.Done():
			return false
		}
	}
}

func (h *helper) onExit() {
	if !h.quitting.Load() {
		h.endOnce.Do(func() { close(h.ended) })
	}
}

func (h *helper) quit() {
	h.quitting.Store(true)
	h.d.ui.Quit()
}

// menuWait is how long onReady waits for the first view.
const menuWait = 2 * time.Second

// loop runs once the tray library has started; built is closed once the
// menu is built from the first view.
func (h *helper) loop(ctx context.Context, built chan<- struct{}) {
	for h.view == nil {
		select {
		case m, ok := <-h.msgs:
			if !h.take(m, ok) {
				h.quit()
				return
			}
		case <-ctx.Done():
			h.quit()
			return
		}
	}
	h.apply(*h.view)
	close(built)
	h.check()
	tick := time.NewTicker(h.d.tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			h.quit()
			return
		case m, ok := <-h.msgs:
			if !ok || m.Type == trayproto.TypeQuit {
				h.quit()
				return
			}
			switch m.Type {
			case trayproto.TypeView:
				h.apply(m)
			case trayproto.TypeNotice:
				h.notice = m.Text
				h.check()
			}
		case id := <-h.clicks:
			h.send(trayproto.Msg{Type: trayproto.TypeClick, ID: id})
		case <-h.d.ui.Lost():
			log.Print("the connection to the session bus was lost")
			h.lost.Store(true)
			h.quit()
			return
		case <-h.ended:
			log.Print("the session is ending")
			h.d.ui.Quit()
			return
		case <-tick.C:
			h.check()
			if h.view != nil {
				h.setIcon(*h.view)
				if h.drawing && !h.d.ui.Draws() {
					// the ui stopped drawing (Windows: its drawer failed):
					// today's text menu, at once rather than at the next view
					h.apply(*h.view)
				}
			}
		}
	}
}

// check reports whether the icon is up (Windows: in the notification
// area; it is added again when it is not) and shows a pending notice once
// it is.
func (h *helper) check() {
	if h.d.ui.Shown() {
		if !h.ready {
			h.ready, h.waitingFor = true, ""
			m := trayproto.Msg{Type: trayproto.TypeReady}
			if h.d.goos == "windows" && h.d.window != nil {
				m.Window = h.d.window() // so that the next helper can remove the icon should this one crash
			}
			h.send(m)
		}
		if h.notice != "" {
			if err := h.d.notify(text.App, h.notice); err != nil && err != notify.ErrUnsupported {
				log.Printf("notice: %v", err)
			}
			h.notice = ""
		}
		return
	}
	if h.ready || h.waitingFor == "" {
		h.ready, h.waitingFor = false, "no taskbar"
		h.send(trayproto.Msg{Type: trayproto.TypeWaiting, Why: h.waitingFor})
	}
}

// sectionsShown are the providers whose section has a visible item.
func sectionsShown(items []trayproto.Item) []string {
	var out []string
	for _, it := range items {
		if s := trayproto.Section(it.ID); s != "" && it.Visible && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return slices.Index(trayproto.Sections(), a) - slices.Index(trayproto.Sections(), b)
	})
	return out
}

// build creates the menu for these sections: each shown section and the
// server line are followed by a separator. The drawn panel (drawing) has
// separators the ui draws, and none after the server line: its actions
// follow it at once (the approved mock). The items are set by apply.
func (h *helper) build(shape []string, byID map[string]trayproto.Item, drawing bool) {
	h.shape, h.drawing, h.items, h.shown, h.drawn = shape, drawing, map[string]uiItem{}, map[string]trayproto.Item{}, map[string]panel.Item{}
	for _, id := range trayproto.ItemIDs {
		sec := trayproto.Section(id)
		if sec != "" && !slices.Contains(shape, sec) {
			continue
		}
		it := h.d.ui.AddItem(id, byID[id].Checkbox)
		h.items[id] = it
		go func(id string, ch <-chan struct{}) {
			for range ch { // ends when the item is removed
				h.clicks <- id
			}
		}(id, it.Clicked())
		if (sec != "" && id == "p."+sec) || (id == "server" && !drawing) {
			h.d.ui.AddSeparator(drawing)
		}
	}
}

// apply shows a view, changing only what differs from what is shown. When
// the set of sections shown changes, the menu is rebuilt (Reset): already
// in a Claude-only build when the first status comes after the 5 s the
// agent waits for it and nobody is signed in ("Sign in to Claude Code"
// joins a menu built without a section; CI tray.menu_reset), and when a
// provider appears or goes.
//
// A ui that draws (Windows) gets the usage panel when the view carries
// one (design 6.2): every visible item is drawn from drawnItems, a
// section's problem line under its header (its own item hidden), the
// server line with the panel's text. Otherwise the menu is today's text
// menu exactly.
func (h *helper) apply(v trayproto.Msg) {
	h.view = &v
	byID := map[string]trayproto.Item{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	var drawn map[string]panel.Item
	if v.Panel != nil && h.d.ui.Draws() {
		drawn = drawnItems(*v.Panel, byID)
		if s, ok := byID["server"]; ok {
			s.Text = v.Panel.Server
			byID["server"] = s
		}
	}
	drawing := drawn != nil
	if shape := sectionsShown(v.Items); h.items == nil || !slices.Equal(shape, h.shape) || drawing != h.drawing {
		if h.items != nil {
			h.d.ui.Reset()
		}
		h.build(shape, byID, drawing)
	}
	for _, id := range trayproto.ItemIDs {
		m, ok := h.items[id]
		if !ok {
			continue
		}
		it, old := byID[id], h.shown[id]
		_, seen := h.shown[id]
		if drawing && hiddenUnderHeader(id, drawn) {
			it.Visible = false
		}
		titled := !seen || it.Text != old.Text
		if titled {
			m.SetTitle(it.Text)
		}
		if !drawing && (!seen || it.Image != old.Image) { // a drawn header draws its picture itself
			if img := menuImage(h.d.goos, it.Image); img != nil || seen {
				m.SetImage(img)
			}
		}
		if !seen || it.Enabled != old.Enabled {
			m.SetEnabled(it.Enabled)
		}
		if it.Checkbox && (!seen || it.Checked != old.Checked) {
			m.SetChecked(it.Checked)
		}
		if d, ok := drawn[id]; ok {
			if last, set := h.drawn[id]; !set || !reflect.DeepEqual(last, d) {
				m.SetDrawn(&d)
				h.drawn[id] = d
			}
		}
		// Windows' tray library shows a hidden item again when its title
		// is set: hide it again then (only there: elsewhere the menu stays
		// exactly as before the drawn panel)
		if !seen || it.Visible != old.Visible || (titled && !it.Visible && h.d.goos == "windows") {
			m.SetVisible(it.Visible)
		}
		h.shown[id] = it
	}
	h.setIcon(v)
	if t := v.Title; tray.ShowsTitle(h.d.goos) && (h.lastTitle == nil || *h.lastTitle != t) {
		h.d.ui.SetNextToIcon(t)
		h.lastTitle = &t
	}
	if h.lastTip == nil || *h.lastTip != v.Tooltip {
		h.d.ui.SetTooltip(v.Tooltip)
		tip := v.Tooltip
		h.lastTip = &tip
	}
}

// setIcon draws the tray icon and sets it when its picture or (Windows)
// the notification area's icon size changed.
func (h *helper) setIcon(v trayproto.Msg) {
	var spec icon.Spec
	if v.Icon != nil {
		spec = icon.Spec{Provider: icon.Provider(v.Icon.Provider), Percent: v.Icon.Percent, Level: usage.Level(v.Icon.Level), Stale: v.Icon.Stale}
	}
	spec.Number = tray.NumberInIcon(h.d.goos, h.d.getenv("XDG_CURRENT_DESKTOP"))
	img := icon.TrayImage(h.d.goos, spec)
	if size := h.d.ui.IconSize(); needIcon(h.lastIcon, h.lastSize, img, size) {
		h.d.ui.SetIcon(img)
		h.lastIcon, h.lastSize = img, size
	}
}

// menuImage is the 16 px picture for an item's Image ("" or a provider
// without artwork: none).
func menuImage(goos, provider string) []byte {
	if provider == "" {
		return nil
	}
	return icon.MenuImage(goos, icon.Provider(provider))
}
