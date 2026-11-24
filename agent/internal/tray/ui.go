//go:build !darwin || cgo

package tray

import (
	"context"
	"log"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/notify"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

// Compiled is true: this build has a tray.
const Compiled = true

const (
	redrawEvery  = 60 * time.Second
	removeArmFor = 10 * time.Second
	titleNotice  = 20 * time.Second // macOS shows the notice as its title
)

// Run shows the tray icon and menu and blocks until Quit, Remove or the end
// of ctx; it returns ErrSessionLost when the tray's connection to the
// session went away (Linux: the session bus). It must be called on the
// main goroutine (macOS draws there).
func Run(ctx context.Context, c Controller, updates <-chan struct{}, notice string) error {
	var lost atomic.Bool
	systray.Run(func() { go loop(ctx, c, updates, notice, &lost) }, func() {})
	if runtime.GOOS == "windows" {
		removeIconFiles(c.IconDir())
	}
	if lost.Load() {
		return ErrSessionLost
	}
	return nil
}

type shown struct {
	icon     []byte
	iconSize int // Windows: the notification area's icon size it was set at
	title    string
	tooltip  string
	items    []Item
}

func loop(ctx context.Context, c Controller, updates <-chan struct{}, notice string, lost *atomic.Bool) {
	menu := make([]*systray.MenuItem, itemCount)
	for id := ItemID(0); id < itemCount; id++ {
		if id == ItemAutostart {
			menu[id] = systray.AddMenuItemCheckbox(text.Autostart, "", false)
		} else {
			menu[id] = systray.AddMenuItem("", "")
		}
		if id == ItemSignIn || id == ItemServer {
			systray.AddSeparator()
		}
	}

	var last shown
	armed := false
	var armTimer, noticeTimer <-chan time.Time
	titleOverride := ""
	if notice != "" {
		if runtime.GOOS == "darwin" {
			titleOverride = text.NoticeDarwin
			noticeTimer = time.After(titleNotice)
		} else if err := notify.Show(text.App, notice); err != nil {
			log.Printf("tray: notice: %v", err)
		}
	}
	draw := func() {
		v := Build(Inputs{Snap: c.Snapshot(), Now: time.Now(), Version: version.Version, HasToggle: c.HasToggle(),
			AutostartOn: c.HasToggle() && c.AutostartOn(), RemoveArmed: armed, DashboardURL: c.DashboardURL()})
		if titleOverride != "" {
			v.Title = titleOverride
		}
		apply(menu, &last, v, c.IconDir())
	}
	draw()
	tick := time.NewTicker(redrawEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			systray.Quit()
			return
		case <-sessionLost():
			lost.Store(true)
			systray.Quit()
			return
		case <-updates:
		case <-tick.C:
		case <-noticeTimer:
			titleOverride, noticeTimer = "", nil
		case <-armTimer:
			armed, armTimer = false, nil
		case <-menu[ItemNewVersion].ClickedCh:
			if u := c.DashboardURL(); u != "" {
				openLogged(u + "/install")
			}
		case <-menu[ItemDashboard].ClickedCh:
			openLogged(c.DashboardURL())
		case <-menu[ItemRefresh].ClickedCh:
			c.Refresh()
		case <-menu[ItemAutostart].ClickedCh:
			if err := c.SetAutostart(!c.AutostartOn()); err != nil {
				log.Printf("tray: start at login: %v", err)
			}
		case <-menu[ItemRemove].ClickedCh:
			if !armed {
				armed, armTimer = true, time.After(removeArmFor)
				break
			}
			if err := c.Remove(); err != nil {
				log.Printf("tray: remove: %v", err)
				_ = notify.Show(text.App, text.RemoveFailed+err.Error())
			}
			systray.Quit()
			return
		case <-menu[ItemQuit].ClickedCh:
			systray.Quit()
			return
		}
		draw()
	}
}

func openLogged(u string) {
	if err := OpenURL(u); err != nil {
		log.Printf("tray: open %s: %v", u, err)
	}
}

// apply changes only what differs from what is shown.
func apply(menu []*systray.MenuItem, last *shown, v View, iconDir string) {
	img := IconImage(runtime.GOOS, os.Getenv("XDG_CURRENT_DESKTOP"), v)
	// On Windows the icon is also set again when the notification area's
	// icon size changed (checked on every redraw, at least once a minute).
	if size := trayIconSize(); needIcon(last.icon, last.iconSize, img, size) {
		setIcon(img, iconDir)
		last.icon, last.iconSize = img, size
	}
	if t, ok := NextToIcon(runtime.GOOS, v); ok && t != last.title {
		setNextToIcon(t)
		last.title = t
	}
	if v.Tooltip != last.tooltip {
		systray.SetTooltip(v.Tooltip)
		last.tooltip = v.Tooltip
	}
	first := last.items == nil
	for i, it := range v.Items {
		m := menu[it.ID]
		var old Item
		if !first {
			old = last.items[i]
		}
		if first || it.Text != old.Text {
			m.SetTitle(it.Text)
		}
		if first || it.Enabled != old.Enabled {
			if it.Enabled {
				m.Enable()
			} else {
				m.Disable()
			}
		}
		if it.ID == ItemAutostart && (first || it.Checked != old.Checked) {
			if it.Checked {
				m.Check()
			} else {
				m.Uncheck()
			}
		}
		if first || it.Visible != old.Visible {
			if it.Visible {
				m.Show()
			} else {
				m.Hide()
			}
		}
	}
	last.items = append([]Item(nil), v.Items...)
}

// setIcon shows img. On Windows the library needs a file: it gets one of
// ours (see iconFile) instead of writing to %TEMP% itself.
func setIcon(img []byte, iconDir string) {
	if runtime.GOOS != "windows" || iconDir == "" {
		systray.SetIcon(img)
		return
	}
	p, err := iconFile(iconDir, img, iconsKeep)
	if err == nil {
		err = systray.SetIconFromFilePath(p)
	}
	if err != nil {
		log.Printf("tray: icon: %v", err)
	}
}
