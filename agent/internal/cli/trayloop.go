package cli

import (
	"context"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

const (
	redrawEvery  = 60 * time.Second // times in the menu ("Updated 16:02") move on
	removeArmFor = 10 * time.Second // the second click on Remove must come within this
	titleNotice  = 20 * time.Second // macOS shows the notice as the menu bar title this long
	firstStatus  = 5 * time.Second  // a fresh install waits this long for the first status before it shows the neutral icon
)

// trayLoop shows the agent through its tray helpers (design 6.4): it keeps
// the helpers to the providers in use, sends them the view whenever it
// changes, handles their clicks and tells the engine whether an icon is
// shown. It never touches the desktop session itself.
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

func testHelpers(env Env) []provider.ID {
	if buildVersion() != "0.0.0-ci" {
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

func (l *trayLoop) inputs() tray.Inputs {
	return tray.Inputs{Snap: l.ctrl.Snapshot(), Now: l.env.Clock.Now(), Version: version.Version, HasToggle: l.ctrl.HasToggle(),
		AutostartOn: l.ctrl.HasToggle() && l.ctrl.AutostartOn(), RemoveArmed: l.armed, DashboardURL: l.ctrl.DashboardURL()}
}

// draw sends the current view to the helpers.
func (l *trayLoop) draw() {
	v := tray.Build(l.inputs())
	if l.title != "" {
		v.Title = l.title
	}
	iv := tray.IconView(v)
	icons := map[provider.ID]supervisor.IconView{"": iv}
	for _, id := range l.wanted {
		icons[id] = iv
		if string(id) != iv.Icon.Provider { // a test helper (testHelpers): the neutral ring
			n := iv
			n.Icon.Provider = ""
			icons[id] = n
		}
	}
	l.sup.Show(tray.Menu(v), icons)
}

// run shows the tray until Quit, Remove or the end of ctx. notice is the
// once-only "running" notice ("" for none).
func (l *trayLoop) run(ctx context.Context, notice string) {
	defer l.sup.Stop()
	l.unused = map[provider.ID]int{}
	l.noticeQueued = notice
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
		case e := <-l.sup.Events():
			if done := l.event(e); done {
				return
			}
		}
		l.draw()
	}
}

// event handles one helper event; true when the agent is to stop.
func (l *trayLoop) event(e supervisor.Event) bool {
	switch {
	case e.Ready, e.Waiting != "", e.Exit != nil:
		mode := "headless"
		if l.sup.AnyReady() {
			mode = "tray"
		}
		l.setMode(mode)
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
	}
	return false
}

// failureNotice shows text through a neutral helper started for it: the
// helpers were stopped before the removal (design 6.4).
func (l *trayLoop) failureNotice(msg string) {
	if l.env.goos() == "darwin" {
		return // no notifications without an app bundle
	}
	s := supervisor.New(supervisor.Options{Spawn: l.env.helperSpawner(l.log), Clock: l.env.Clock, Log: l.log})
	defer s.Stop()
	s.Notice(msg)
	s.Want(nil)
	s.Show(tray.Menu(tray.Build(l.inputs())), nil)
	deadline := l.env.Clock.After(15 * time.Second)
	for {
		select {
		case e := <-s.Events():
			if e.Ready {
				<-l.env.Clock.After(time.Second) // the notice is handed over
				return
			}
			if e.Exit != nil {
				return
			}
		case <-deadline:
			return
		}
	}
}

// helperSpawner starts the agent's own executable as a tray helper.
func (env Env) helperSpawner(lg *log.Logger) func(p provider.ID, uid uint32, token string) (supervisor.Proc, error) {
	if env.Helper != nil {
		return env.Helper
	}
	iconDir := func(p provider.ID) string {
		n := string(p)
		if n == "" {
			n = "none"
		}
		return filepath.Join(env.Dirs.File(paths.TrayIconsDir), n)
	}
	return supervisor.ExecSpawner(env.Exe, iconDir, lg)
}
