//go:build !darwin || cgo

package tray

import (
	"bytes"
	"context"
	"log"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/icon"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/notify"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/text"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/version"
)

// Compiled is true: this build has a tray.
const Compiled = true

const (
	redrawEvery  = 60 * time.Second
	removeArmFor = 10 * time.Second
	titleNotice  = 20 * time.Second // macOS shows the notice as its title
)

// Run shows the tray icon and menu and blocks until Quit, Remove or the end
// of ctx. It must be called on the main goroutine (macOS draws there).
func Run(ctx context.Context, c Controller, updates <-chan struct{}, notice string) error {
	systray.Run(func() { go loop(ctx, c, updates, notice) }, func() {})
	if runtime.GOOS == "windows" {
		removeIconFiles(c.IconDir())
	}
	return nil
}

type shown struct {
	icon    []byte
	title   string
	tooltip string
	items   []Item
}

func loop(ctx context.Context, c Controller, updates <-chan struct{}, notice string) {
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
	img := icon.TrayImage(runtime.GOOS, v.Icon)
	if !bytes.Equal(img, last.icon) {
		setIcon(img, iconDir)
		last.icon = img
	}
	if runtime.GOOS == "darwin" && v.Title != last.title {
		systray.SetTitle(v.Title)
		last.title = v.Title
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
