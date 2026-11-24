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
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/notify"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Options are a helper's command line and its token.
type Options struct {
	Provider string // "claude", "codex", or "" for the neutral helper
	UID      uint32 // the Windows notification icon id
	IconDir  string // Windows: where its icon files go (in the data folder)
	Token    string // the value its first line must carry (from trayproto.EnvToken)
}

// ExitNoTray: this build has no tray (macOS without cgo).
const ExitNoTray = 3

// Run shows one tray icon and blocks until quit, the end of standard
// input, or the loss or end of the session; it returns the exit code. It
// must run on the main goroutine (macOS draws there).
func Run(ctx context.Context, in io.Reader, out io.Writer, o Options) int {
	return runWith(ctx, in, out, o, deps{
		goos:         runtime.GOOS,
		getenv:       os.Getenv,
		ui:           newUI(o),
		available:    func(ctx context.Context) (bool, string) { return Available(ctx, os.Getenv) },
		waitHost:     func(ctx context.Context) bool { return WaitForHost(ctx, 0) },
		notify:       notify.Show,
		tick:         2 * time.Second,
		readyTimeout: 60 * time.Second,
		giveUp:       func() { log.Print("the tray did not start within a minute; ending"); os.Exit(1) },
	})
}

// deps are what a helper run depends on; tests replace them.
type deps struct {
	goos      string
	getenv    func(string) string
	ui        ui // nil: no tray in this build
	available func(ctx context.Context) (bool, string)
	waitHost  func(ctx context.Context) bool
	notify    func(title, text string) error
	// tick: how often the icon's state is checked (Windows: still in the
	// notification area, its size).
	tick time.Duration
	// readyTimeout: the tray library must have started by then, or giveUp.
	readyTimeout time.Duration
	giveUp       func()
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
	shape      []string // the sections the menu was built with
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

func runWith(ctx context.Context, in io.Reader, out io.Writer, o Options, d deps) int {
	br := bufio.NewReader(in)
	first, err := trayproto.Decode(br)
	if err != nil || first.Type != trayproto.TypeHello || first.V != trayproto.Version || o.Token == "" || first.Token != o.Token {
		log.Print("tray-helper: this command is started by the agent itself")
		return trayproto.ExitRefused
	}
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
	d.ui.Run(func() { close(started); h.loop(ctx) }, h.onExit)
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

// loop runs once the tray library has started.
func (h *helper) loop(ctx context.Context) {
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
			h.send(trayproto.Msg{Type: trayproto.TypeReady})
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
// server line are followed by a separator. The items are set by apply.
func (h *helper) build(shape []string, byID map[string]trayproto.Item) {
	h.shape, h.items, h.shown = shape, map[string]uiItem{}, map[string]trayproto.Item{}
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
		if (sec != "" && id == "p."+sec) || id == "server" {
			h.d.ui.AddSeparator()
		}
	}
}

// apply shows a view, changing only what differs from what is shown.
func (h *helper) apply(v trayproto.Msg) {
	h.view = &v
	byID := map[string]trayproto.Item{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	if shape := sectionsShown(v.Items); h.items == nil || !slices.Equal(shape, h.shape) {
		if h.items != nil {
			h.d.ui.Reset()
		}
		h.build(shape, byID)
	}
	for _, id := range trayproto.ItemIDs {
		m, ok := h.items[id]
		if !ok {
			continue
		}
		it, old := byID[id], h.shown[id]
		_, seen := h.shown[id]
		if !seen || it.Text != old.Text {
			m.SetTitle(it.Text)
		}
		if !seen || it.Image != old.Image {
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
		if !seen || it.Visible != old.Visible {
			m.SetVisible(it.Visible)
		}
		h.shown[id] = it
	}
	h.setIcon(v)
	if t, ok := tray.NextToIcon(h.d.goos, tray.View{Title: v.Title}); ok && (h.lastTitle == nil || *h.lastTitle != t) {
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
// without one: none). None yet.
func menuImage(goos, provider string) []byte { return nil }
