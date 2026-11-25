//go:build !darwin || cgo

package trayhelper

import (
	"log"
	"runtime"

	"fyne.io/systray"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// Compiled is true: this build has a tray.
const Compiled = true

// systrayUI is the patched fyne systray copy (third_party/systray).
type systrayUI struct {
	iconDir string
	drawer  itemDrawer // nil: this OS's menu is text items
}

func newUI(o Options) ui {
	setTrayID(o.UID)
	return systrayUI{iconDir: o.IconDir, drawer: newItemDrawer()}
}

func (systrayUI) Run(onReady, onExit func()) { systray.Run(onReady, onExit) }
func (systrayUI) Quit()                      { systray.Quit() }
func (u systrayUI) Draws() bool              { return u.drawer != nil && !u.drawer.Failed() }
func (systrayUI) SetNextToIcon(s string)     { setNextToIcon(s) }
func (systrayUI) SetTooltip(s string)        { systray.SetTooltip(s) }
func (systrayUI) Lost() <-chan struct{}      { return sessionLost() }
func (systrayUI) Shown() bool                { return iconShown() }
func (systrayUI) IconSize() int              { return trayIconSize() }

// Reset removes every item; the drawer forgets what they drew.
func (u systrayUI) Reset() {
	systray.ResetMenu()
	if u.drawer != nil {
		u.drawer.Clear()
	}
}

func (u systrayUI) AddItem(_ string, checkbox bool) uiItem {
	if checkbox {
		return systrayItem{systray.AddMenuItemCheckbox("", "", false), u.iconDir, u.drawer}
	}
	return systrayItem{systray.AddMenuItem("", ""), u.iconDir, u.drawer}
}

func (u systrayUI) AddSeparator(drawn bool) {
	if drawn && u.drawer != nil {
		systray.AddOwnerDrawnSeparator()
		return
	}
	systray.AddSeparator()
}

// SetIcon shows img. On Windows the library needs a file: it gets one of
// ours in the helper's icon folder (see iconFile) instead of writing to
// %TEMP% itself.
func (u systrayUI) SetIcon(img []byte) {
	if runtime.GOOS != "windows" || u.iconDir == "" {
		systray.SetIcon(img)
		return
	}
	p, err := iconFile(u.iconDir, img, iconsKeep)
	if err == nil {
		err = systray.SetIconFromFilePath(p)
	}
	if err != nil {
		log.Printf("icon: %v", err)
	}
}

type systrayItem struct {
	m       *systray.MenuItem
	iconDir string
	drawer  itemDrawer
}

// SetDrawn: the drawer gets the item's data first, then the menu item
// becomes owner-drawn (or a text item again).
func (i systrayItem) SetDrawn(it *panel.Item) {
	if i.drawer == nil {
		return
	}
	i.drawer.Set(i.m.ID(), it)
	i.m.SetOwnerDrawn(it != nil)
}

func (i systrayItem) SetTitle(s string) { i.m.SetTitle(s) }
func (i systrayItem) SetEnabled(b bool) {
	if b {
		i.m.Enable()
	} else {
		i.m.Disable()
	}
}
func (i systrayItem) SetVisible(b bool) {
	if b {
		i.m.Show()
	} else {
		i.m.Hide()
	}
}
func (i systrayItem) SetChecked(b bool) {
	if b {
		i.m.Check()
	} else {
		i.m.Uncheck()
	}
}
func (i systrayItem) Clicked() <-chan struct{} { return i.m.ClickedCh }

// SetImage sets the item's 16 px picture (Windows: from a file of ours).
func (i systrayItem) SetImage(img []byte) {
	if len(img) == 0 {
		return
	}
	if runtime.GOOS != "windows" || i.iconDir == "" {
		i.m.SetIcon(img)
		return
	}
	p, err := iconFile(i.iconDir, img, iconsKeep)
	if err == nil {
		err = i.m.SetIconFromFilePath(p)
	}
	if err != nil {
		log.Printf("menu icon: %v", err)
	}
}
