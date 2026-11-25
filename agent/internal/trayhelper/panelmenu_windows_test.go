package trayhelper

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// panelMenuChildEnv starts TestPanelMenuChild (read by this test file only).
const panelMenuChildEnv = "TRAYHELPER_PANELMENU_CHILD"

var (
	pPMRedrawWindow   = odUser32.NewProc("RedrawWindow")
	pPMGetMenuItemRct = odUser32.NewProc("GetMenuItemRect")
	pPMSetCursorPos   = odUser32.NewProc("SetCursorPos")
)

// panelMenuResult is what the child saw.
type panelMenuResult struct {
	Error          string
	LoadsAtOpen    int32  // the mode read when the menu opened
	DrawsAfter     int    // draws of the item after its new value, menu still open
	TextWhileOpen  string // what it drew then
	Closed         bool   // the menu closed by itself on the colour-mode change
	Ends           int    // EndMenu calls
	TextNextOpen   string // what the item drew at the next open
	BackgroundNext uint32 // the menu's background then
}

// TestPanelMenuChild is the child of TestOpenPanelMenu: the patched tray
// with the real drawer on this desktop.
func TestPanelMenuChild(t *testing.T) {
	if os.Getenv(panelMenuChildEnv) == "" {
		t.Skip("child of TestOpenPanelMenu")
	}
	runtime.LockOSThread()
	var dark atomic.Bool
	var loads atomic.Int32
	d := newTestDrawer(func() (menuTheme, panel.Palette) {
		loads.Add(1)
		return menuTheme{Dark: dark.Load()}, panel.LightPalette
	})
	d.endMenu = func() {
		d.mu.Lock()
		d.ends++
		d.mu.Unlock()
		pwEndMenu.Call()
	}
	draws := map[uint32]int{}
	d.trace = func(id uint32, ops []panel.Op) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.ops[id] = ops
		draws[id]++
	}
	countOf := func(id uint32) int {
		d.mu.Lock()
		defer d.mu.Unlock()
		return draws[id]
	}
	textOf := func(id uint32) string {
		for _, o := range d.drawn(id) {
			if o.Kind == panel.Text {
				return o.Text
			}
		}
		return ""
	}
	systray.SetMenuDrawer(d.winDrawer)
	var res panelMenuResult
	systray.Run(func() {
		defer systray.Quit()
		ui := systrayUI{drawer: d.winDrawer}
		a := ui.AddItem("refresh", false).(systrayItem)
		q := ui.AddItem("quit", false).(systrayItem)
		before := panel.Item{Kind: "action", Text: "Refresh now", Enabled: true}
		after := panel.Item{Kind: "action", Text: "Refreshing…"}
		quit := panel.Item{Kind: "action", Text: "Quit", Enabled: true}
		a.SetDrawn(&before)
		q.SetDrawn(&quit)
		id := a.m.ID()

		n := loads.Load()
		w, m := openMenu()
		if w == 0 {
			res.Error = "no menu window"
			return
		}
		res.LoadsAtOpen = loads.Load() - n
		time.Sleep(500 * time.Millisecond)
		// a new view while the menu is open, then the item drawn again
		// (a repaint, the pointer on it)
		c := countOf(id)
		a.SetDrawn(&after)
		const rdwInvalidate, rdwErase, rdwUpdateNow = 0x1, 0x4, 0x100
		pPMRedrawWindow.Call(uintptr(w), 0, 0, rdwInvalidate|rdwErase|rdwUpdateNow)
		var r systray.Rect
		if ok, _, _ := pPMGetMenuItemRct.Call(0, uintptr(m), 0, uintptr(unsafe.Pointer(&r))); ok != 0 {
			pPMSetCursorPos.Call(uintptr((r.Left+r.Right)/2), uintptr((r.Top+r.Bottom)/2))
		}
		time.Sleep(500 * time.Millisecond)
		res.DrawsAfter = countOf(id) - c
		res.TextWhileOpen = textOf(id)

		// the colour mode changes while the menu is open
		dark.Store(true)
		const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
		area, _ := windows.UTF16PtrFromString("ImmersiveColorSet")
		var rr uintptr
		pODSendMessageTimeou.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(area)), smtoAbortIfHung, 3000, uintptr(unsafe.Pointer(&rr)))
		for range 40 {
			if ownMenuWindow() == 0 {
				res.Closed = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		res.Ends = d.ended()
		if !res.Closed {
			closeMenu(w)
		}

		// the next open: the new value, in the new mode
		w, m = openMenu()
		if w == 0 {
			res.Error = "no menu window the second time"
			return
		}
		time.Sleep(500 * time.Millisecond)
		res.TextNextOpen = textOf(id)
		res.BackgroundNext = menuBackground(m)
		closeMenu(w)
	}, func() {
		b, _ := json.Marshal(res)
		os.Stdout.WriteString("\npanelmenu-result " + string(b) + "\n")
	})
}

// Reviews A-m2, A-m3, A-m14 on the real menu with the real drawer: the
// mode is read when the menu opens; an open menu keeps the values it
// opened with although the item is drawn again after a new view; a change
// of the colour mode closes the open menu (EndMenu on the tray's thread);
// the next open draws the new value in the new mode.
func TestOpenPanelMenu(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestPanelMenuChild$", "-test.v")
	cmd.Env = append(os.Environ(), panelMenuChildEnv+"=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan *panelMenuResult, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var got *panelMenuResult
		for sc.Scan() {
			if s, ok := strings.CutPrefix(sc.Text(), "panelmenu-result "); ok {
				got = &panelMenuResult{}
				if err := json.Unmarshal([]byte(s), got); err != nil {
					got.Error = "result: " + err.Error()
				}
			}
		}
		done <- got
	}()
	var res *panelMenuResult
	select {
	case res = <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the child did not finish within 60 s")
	}
	_ = cmd.Wait()
	if res == nil {
		t.Fatal("the child reported nothing")
	}
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	t.Logf("%+v", *res)
	if res.LoadsAtOpen < 1 {
		t.Fatal("the mode was not read when the menu opened")
	}
	if res.DrawsAfter == 0 {
		t.Fatal("the item was not drawn again while the menu was open")
	}
	eq(t, res.TextWhileOpen, "Refresh now")
	if !res.Closed || res.Ends != 1 {
		t.Fatalf("on the mode change: closed %v, EndMenu %d times", res.Closed, res.Ends)
	}
	eq(t, res.TextNextOpen, "Refreshing…")
	eq(t, res.BackgroundNext, colorRef(panel.DarkPalette.Background))
}
