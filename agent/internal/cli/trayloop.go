package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/atomicfile"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

const (
	redrawEvery  = 60 * time.Second // times in the menu ("Updated 16:02") move on
	removeArmFor = 10 * time.Second // the second click on Remove must come within this
	titleNotice  = 20 * time.Second // macOS shows the notice as the menu bar title this long
	firstStatus  = 5 * time.Second  // a fresh install waits this long for the first status before it shows the neutral icon
	// the GNOME usage panel (drawn-panel design 6.4; plan PR-11)
	attachWait  = 10 * time.Second // with the gnome-panel-attached marker the icons wait this long for the panel
	detachGrace = 10 * time.Second // after the panel went away the icons wait this long for it to come back
)

// trayLoop shows the agent through its tray helpers (design 6.4): it keeps
// the helpers to the providers in use, sends them the view whenever it
// changes, handles their clicks and tells the engine whether an icon is
// shown. It never touches the desktop session itself, except for the one
// notification after a failed Remove (the helpers are gone by then).
type trayLoop struct {
	env     Env
	ctrl    *controller
	sup     *supervisor.Supervisor
	updates <-chan struct{}
	log     *log.Logger
	// setMode tells the engine what client.mode to report.
	setMode func(mode string)
	// remove runs the tray's Remove (helpers stopped first).
	remove func() error

	known    []provider.ID // the providers the engine reads, in menu order
	extra    []provider.ID // CI builds only: helpers shown whatever is detected (testHelpers)
	wanted   []provider.ID
	unused   map[provider.ID]int // status updates in a row that a wanted provider was not in use
	haveSnap bool

	armed        bool
	armTimer     <-chan time.Time
	title        string // macOS: the notice shown as the title
	noticeTimer  <-chan time.Time
	noticeQueued string
	readyOnce    bool
	waitLogged   string

	// the last usage panel sent (its serial 0) and its serial (plan PR-6)
	lastPanel   *trayproto.Panel
	panelSerial uint64

	// The GNOME usage panel (design 6.4, 6.7): the bridge is wanted; the
	// icon helpers are held back (Supervisor.SetPanelAttached); the bridge
	// says the extension is attached; hold ends an attach wait or a detach
	// grace (the icons come then); a GNOME Shell the panel supports
	// answered the running bridge and has not left since (only then may
	// the gnome-panel-attached marker go: review I1); an attach wait was
	// started since then (a lost session bus starts one only once).
	bridge  bool
	held    bool
	panelOn bool
	hold    <-chan time.Time
	shellUp bool
	waited  bool
	// tests: called once an event has been handled
	handled func(supervisor.Event)
}

// wantsBridge: the GNOME bridge runs in a GNOME session on Linux (design
// 6.4: the session decides, not whether the extension's files are there);
// XDG_CURRENT_DESKTOP is a list such as "ubuntu:GNOME".
func wantsBridge(goos string, getenv func(string) string) bool {
	return goos == "linux" && strings.Contains(strings.ToUpper(getenv("XDG_CURRENT_DESKTOP")), "GNOME")
}

// panelEnableOnce: the bridge may turn the panel extension on, because no
// bridge has done so yet (no gnome-panel-enabled marker; design 6.6). Asked
// at each start of the bridge (ruling F15).
func (env Env) panelEnableOnce() bool {
	_, err := os.Stat(env.Dirs.File(paths.PanelEnabledFile))
	return errors.Is(err, fs.ErrNotExist)
}

// inUse: the provider's sign-in is there (an expired one too: the account
// is), so its icon is shown (design 6.3, the v0.8 rule for Clawd).
func inUse(ps status.ProviderStatus) bool { return ps.State == "ok" || ps.State == "expired" }

// want updates the helpers wanted from a snapshot. A provider comes in at
// once; it goes only after two updates in a row without it, so that a
// sign-in file being rewritten does not restart its icon.
func (l *trayLoop) want(s status.Snapshot) {
	var next []provider.ID
	for _, id := range l.known {
		ps, ok := s.Providers[id]
		switch {
		case ok && inUse(ps):
			l.unused[id] = 0
			next = append(next, id)
		case slices.Contains(l.wanted, id):
			l.unused[id]++
			if l.unused[id] < 2 {
				next = append(next, id)
			}
		}
	}
	for _, id := range l.extra {
		if !slices.Contains(next, id) {
			next = append(next, id)
		}
	}
	l.wanted = next
	l.sup.Want(next)
}

// testHelpersEnv names helpers a CI build (version 0.0.0-ci) shows whatever
// is detected, e.g. "claude,codex": CI checks that two helpers show two
// icons. A helper of a provider that is not in use shows the neutral ring.
// Any other build ignores it.
const testHelpersEnv = "AI_USAGE_TEST_FAKE_PROVIDERS"

// buildVersion is this build's version (tests stand in for a CI build).
var buildVersion = func() string { return version.Version }

// ciVersion is the version of a CI build, the only build that reads the
// AI_USAGE_TEST_* switches (testSwitches). It is a variable so that the
// release check's own test can build an executable that wrongly behaves
// as a CI build under a release's version (tools/scripts_test.go).
var ciVersion = "0.0.0-ci"

// isCIBuild: this build is a CI build.
func isCIBuild() bool { return buildVersion() == ciVersion }

func testHelpers(env Env) []provider.ID {
	if !isCIBuild() {
		return nil
	}
	var out []provider.ID
	for _, f := range strings.Split(env.Getenv(testHelpersEnv), ",") {
		id := provider.ID(strings.TrimSpace(f))
		if (id == provider.Claude || id == provider.Codex) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (l *trayLoop) inputs() tray.MenuInputs {
	return tray.MenuInputs{Snap: l.ctrl.Snapshot(), Now: l.env.Clock.Now(), Version: version.Version, HasToggle: l.ctrl.HasToggle(),
		AutostartOn: l.ctrl.HasToggle() && l.ctrl.AutostartOn(), RemoveArmed: l.armed, DashboardURL: l.ctrl.DashboardURL()}
}

// draw sends the current view to the helpers: one menu for all, each its
// provider's icon (design 6.5, 6.6).
func (l *trayLoop) draw() {
	in := l.inputs()
	icons := map[provider.ID]supervisor.IconView{}
	for _, id := range append(slices.Clone(l.wanted), "") {
		v := tray.BuildIcon(id, in)
		if l.title != "" {
			v.Title = l.title
		}
		icons[id] = v
	}
	items := tray.BuildMenu(in)
	l.dumpView(items)
	panel := l.numberPanel(tray.BuildPanel(in))
	l.sup.Show(items, icons, &panel)
}

// numberPanel gives p its serial (plan PR-6): one more than the last
// panel's when p differs from it (its serial aside), the same otherwise;
// the first panel is 1. The GNOME extension redraws on a new serial.
func (l *trayLoop) numberPanel(p trayproto.Panel) trayproto.Panel {
	p.Serial = 0
	if l.lastPanel == nil || !reflect.DeepEqual(*l.lastPanel, p) {
		l.panelSerial++
		last := p
		l.lastPanel = &last
	}
	p.Serial = l.panelSerial
	return p
}

// testDumpViewEnv names a file where a CI build (version 0.0.0-ci) writes
// the menu it shows, as JSON, each time it is drawn (plan Task 11: the
// -schema1-only check reads the Codex line). Any other build ignores it.
const testDumpViewEnv = "AI_USAGE_TEST_DUMP_VIEW"

// dumpView writes the visible menu items to testDumpViewEnv's file.
func (l *trayLoop) dumpView(items []trayproto.Item) {
	p := l.env.Getenv(testDumpViewEnv)
	if p == "" || !isCIBuild() {
		return
	}
	type item struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		Visible bool   `json:"visible"`
	}
	var v struct {
		Items []item `json:"items"`
	}
	for _, it := range items {
		if it.Visible {
			v.Items = append(v.Items, item{it.ID, it.Text, true})
		}
	}
	b, err := json.Marshal(v)
	if err == nil {
		err = atomicfile.Write(p, b, 0o600)
	}
	if err != nil {
		l.log.Printf("test view dump: %v", err)
	}
}

// run shows the tray until Quit, Remove or the end of ctx. notice is the
// once-only "running" notice ("" for none).
func (l *trayLoop) run(ctx context.Context, notice string) {
	defer l.sup.Stop()
	l.unused = map[provider.ID]int{}
	l.noticeQueued = notice
	if l.bridge {
		// the panel was attached in the session before: the icons wait for
		// it a while instead of flashing up (design 6.4)
		if l.marked() {
			l.holdIcons(attachWait)
			l.waited = true
		}
		l.sup.WantBridge(true, true)
	}
	s := l.ctrl.Snapshot()
	var wait <-chan time.Time
	if len(s.Providers) > 0 {
		l.haveSnap = true
		l.want(s)
	} else {
		wait = l.env.Clock.After(firstStatus)
	}
	if notice != "" && l.env.goos() != "darwin" {
		l.sup.Notice(notice)
	}
	l.draw()
	tick := l.env.Clock.After(redrawEvery)
	for {
		var ev *supervisor.Event
		select {
		case <-ctx.Done():
			return
		case <-l.updates:
			if s := l.ctrl.Snapshot(); len(s.Providers) > 0 || l.haveSnap {
				l.haveSnap = true
				l.want(s)
			}
		case <-wait:
			wait = nil
			if !l.haveSnap {
				l.sup.Want(nil)
			}
		case <-tick:
			tick = l.env.Clock.After(redrawEvery)
		case <-l.noticeTimer:
			l.title, l.noticeTimer = "", nil
		case <-l.armTimer:
			l.armed, l.armTimer = false, nil
		case <-l.hold:
			l.hold = nil
			if !l.panelOn {
				l.log.Print("usage panel: not attached; showing the tray icon")
				l.release()
			}
		case e := <-l.sup.Events():
			if done := l.event(e); done {
				return
			}
			ev = &e
		}
		l.draw()
		if ev != nil && l.handled != nil {
			l.handled(*ev)
		}
	}
}

// event handles one helper event; true when the agent is to stop.
func (l *trayLoop) event(e supervisor.Event) bool {
	if e.Provider == supervisor.BridgeSlot && e.Click == "" {
		l.bridgeEvent(e)
		l.setMode(l.mode())
		return false
	}
	switch {
	case e.Ready, e.Waiting != "", e.Exit != nil:
		l.setMode(l.mode())
		if e.Waiting != "" && e.Waiting != l.waitLogged {
			l.log.Printf("no tray (%s): running without an icon", e.Waiting)
			l.waitLogged = e.Waiting
		}
		if e.Ready {
			if !l.readyOnce || l.waitLogged != "" {
				l.log.Print("tray found: showing the icon")
			}
			l.waitLogged = ""
			if !l.readyOnce {
				l.readyOnce = true
				if l.noticeQueued != "" && l.env.goos() == "darwin" {
					l.title, l.noticeTimer = text.NoticeDarwin, l.env.Clock.After(titleNotice)
				}
				l.noticeQueued = ""
			}
		}
	case e.Click != "":
		return l.click(e.Click)
	}
	return false
}

// mode is client.mode: "tray" while the panel is attached or an icon is
// up (design 6.4).
func (l *trayLoop) mode() string {
	if l.panelOn || l.sup.AnyReady() {
		return "tray"
	}
	return "headless"
}

// bridgeEvent follows the bridge (design 6.4, 6.7; plan PR-11): attached,
// the icons go; gone after a Ping lapse, or the bridge stopped after
// repeated failures or found no supported shell, they come at once; gone
// otherwise (a Detach, the shell's owner left, the bridge ended), they
// come after detachGrace unless it attaches again; the bridge ending with
// a lost session bus while the marker is there holds them anew for
// attachWait (a logout: the next login's panel), once: the bridge started
// again on a bus that is not back ends with 75 again and again (review
// I1).
func (l *trayLoop) bridgeEvent(e supervisor.Event) {
	switch {
	case e.Enabled:
		l.markEnabled()
	case e.Supported != nil && *e.Supported:
		l.shellUp, l.waited = true, false
	case e.Attached != nil && *e.Attached:
		if !l.panelOn {
			l.log.Print("usage panel attached: no tray icon")
		}
		l.panelOn, l.hold = true, nil
		l.shellUp, l.waited = true, false
		if !l.held {
			l.held = true
			l.sup.SetPanelAttached(true)
		}
		l.mark()
	case e.Attached != nil:
		was := l.panelOn
		l.panelOn = false
		if e.Why == "owner" {
			// the shell left the bus (a restart, a logout); the bridge says so
			// also when the extension had detached before (review H1)
			l.shellUp = false
		}
		if e.Why == "ping" {
			l.log.Print("usage panel: the extension stopped answering; showing the tray icon")
			l.release()
			return
		}
		if was {
			l.log.Printf("usage panel went away (%s)", e.Why)
		} else if e.Why == "owner" {
			l.log.Print("usage panel: GNOME Shell left the session bus")
		}
		l.grace()
	case e.Supported != nil:
		l.log.Printf("usage panel: not on this desktop (%s)", e.Why)
		l.panelOn, l.shellUp = false, false
		l.release()
		if strings.HasPrefix(e.Why, "unsupported ") {
			// the shell answered with a version the panel does not support:
			// no panel attaches on this desktop, the next start shows the
			// icons at once ("no shell", "no shell version": a session not
			// up yet, the marker stays; ruling of 4b fix round 2)
			l.unmark()
		}
	case e.Exit != nil:
		l.panelOn, l.shellUp = false, false
		switch {
		case *e.Exit == trayproto.ExitSessionLost && l.marked():
			if !l.waited {
				l.waited = true
				l.holdIcons(attachWait)
			}
		case e.Stopped:
			l.release()
		default:
			l.grace()
		}
	}
}

// holdIcons holds the icon helpers back for at most d.
func (l *trayLoop) holdIcons(d time.Duration) {
	if !l.held {
		l.held = true
		l.sup.SetPanelAttached(true)
	}
	l.hold = l.env.Clock.After(d)
}

// grace gives a panel that went away detachGrace to come back.
func (l *trayLoop) grace() {
	if l.held && l.hold == nil {
		l.hold = l.env.Clock.After(detachGrace)
	}
}

// release shows the icons again. It forgets that the panel was attached
// (removes the marker) only while a supported shell answers the bridge:
// then the panel could have attached and did not. Otherwise (no shell
// yet, a logout, no bridge on a bus) the marker stays for the next start
// (design 6.4: not removed when a session ends; review I1).
func (l *trayLoop) release() {
	l.hold = nil
	if l.held {
		l.held = false
		l.sup.SetPanelAttached(false)
	}
	if l.shellUp {
		l.unmark()
	}
}

// unmark removes the gnome-panel-attached marker.
func (l *trayLoop) unmark() {
	if err := os.Remove(l.env.Dirs.File(paths.PanelAttachedFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		l.log.Printf("usage panel: %v", err)
	}
}

// marked: the gnome-panel-attached marker is there.
func (l *trayLoop) marked() bool {
	_, err := os.Stat(l.env.Dirs.File(paths.PanelAttachedFile))
	return err == nil
}

func (l *trayLoop) mark() {
	if l.marked() {
		return
	}
	if err := os.WriteFile(l.env.Dirs.File(paths.PanelAttachedFile), nil, 0o600); err != nil {
		l.log.Printf("usage panel: %v", err)
	}
}

// markEnabled writes the gnome-panel-enabled marker once: no bridge turns
// the extension on again (design 6.6).
func (l *trayLoop) markEnabled() {
	p := l.env.Dirs.File(paths.PanelEnabledFile)
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	l.log.Print("usage panel: the extension was turned on")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		l.log.Printf("usage panel: %v", err)
	}
}

func (l *trayLoop) open(u string) {
	if u == "" {
		return
	}
	open := l.env.OpenURL
	if open == nil {
		open = tray.OpenURL
	}
	if err := open(u); err != nil {
		l.log.Printf("tray: open %s: %v", u, err)
	}
}

func (l *trayLoop) click(id string) bool {
	switch id {
	case "newversion":
		if u := l.ctrl.DashboardURL(); u != "" {
			l.open(u + "/install")
		}
	case "dashboard":
		l.open(l.ctrl.DashboardURL())
	case "refresh":
		l.ctrl.Refresh()
	case "autostart":
		if err := l.ctrl.SetAutostart(!l.ctrl.AutostartOn()); err != nil {
			l.log.Printf("tray: start at login: %v", err)
		}
	case "remove":
		if !l.armed {
			l.armed, l.armTimer = true, l.env.Clock.After(removeArmFor)
			return false
		}
		if err := l.remove(); err != nil {
			l.log.Printf("tray: remove: %v", err)
			l.failureNotice(text.RemoveFailed + err.Error())
		}
		return true
	case "quit":
		return true
	default:
		l.open(tray.RowURL(l.inputs(), id)) // a usage row: its account's page
	}
	return false
}

// failureNotice shows msg as an OS notification from this process (as v0.8
// did): the helpers were stopped before the removal, and a new one would
// be started from an executable the removal may have deleted (Linux) or
// moved away (Windows).
func (l *trayLoop) failureNotice(msg string) {
	if l.env.goos() == "darwin" {
		return // no notifications without an app bundle
	}
	if err := l.env.notify(text.App, msg); err != nil {
		l.log.Printf("tray: could not show the removal failure: %v", err)
	}
}

// helperSpawner starts the agent's own executable as a tray helper.
func (env Env) helperSpawner(lg *log.Logger) func(p provider.ID, uid uint32, token string) (supervisor.Proc, error) {
	if env.Helper != nil {
		return env.Helper
	}
	return supervisor.ExecSpawner(env.Exe, lg, env.HelperEnv...)
}

// iconDir is the folder for p's Windows icon files.
func (env Env) iconDir(p provider.ID) string {
	n := string(p)
	if n == "" {
		n = "none"
	}
	return filepath.Join(env.Dirs.File(paths.TrayIconsDir), n)
}

// watchesDesktop: a helper stopped after repeated failures is replaced by
// one that watches the desktop (supervisor.Options.Standby): Linux sees a
// tray host appear on the session bus, Windows the taskbar's
// TaskbarCreated; macOS has nothing to watch.
func watchesDesktop(goos string) bool { return goos == "linux" || goos == "windows" }
