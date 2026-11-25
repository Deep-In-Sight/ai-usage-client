package trayhelper

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

// ownerDrawChildEnv starts TestOwnerDrawChild (read by this test file only).
const ownerDrawChildEnv = "TRAYHELPER_OWNERDRAW_CHILD"

var (
	odUser32             = windows.NewLazySystemDLL("user32.dll")
	pODFindWindowExW     = odUser32.NewProc("FindWindowExW")
	pODIsWindowVisible   = odUser32.NewProc("IsWindowVisible")
	pODPostMessageW      = odUser32.NewProc("PostMessageW")
	pODSendMessageW      = odUser32.NewProc("SendMessageW")
	pODSendMessageTimeou = odUser32.NewProc("SendMessageTimeoutW")
	pODGetMenuItemInfoW  = odUser32.NewProc("GetMenuItemInfoW")
	pODGetMenuInfo       = odUser32.NewProc("GetMenuInfo")
	pODGetWindowThreadID = odUser32.NewProc("GetWindowThreadProcessId")
	pODGetObjectW        = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetObjectW")
)

// odMenuItemInfo is MENUITEMINFOW.
type odMenuItemInfo struct {
	Size, Mask, Type, State     uint32
	ID                          uint32
	SubMenu, Checked, Unchecked windows.Handle
	ItemData                    uintptr
	TypeData                    *uint16
	Cch                         uint32
	BMPItem                     windows.Handle
}

// fakeMenuDrawer records what the tray asks of a drawer.
type fakeMenuDrawer struct {
	mu       sync.Mutex
	measured map[uint32][2]int32 // id -> the size it answered
	drawn    map[uint32]systray.Rect
	seps     []int32 // heights of the separators drawn
	sepMeas  int
	areas    []string
	measures int
	grow     int32           // added to every height measured from now on
	dpis     map[uint32]bool // every DPI Measure and Draw were given
}

func (f *fakeMenuDrawer) Measure(id, dpi uint32) (int32, int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, h := int32(200+id), int32(20+id%7)+f.grow
	f.measured[id] = [2]int32{w, h}
	f.measures++
	f.dpis[dpi] = true
	return w, h
}

func (f *fakeMenuDrawer) Draw(id uint32, hdc uintptr, rc systray.Rect, state, dpi uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drawn[id] = rc
	f.dpis[dpi] = true
}

func (f *fakeMenuDrawer) SettingChanged(area string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.areas = append(f.areas, area)
}

func (f *fakeMenuDrawer) MeasureSeparator(dpi uint32) (int32, int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sepMeas++
	return 10, 13
}

func (f *fakeMenuDrawer) DrawSeparator(hdc uintptr, rc systray.Rect, dpi uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seps = append(f.seps, rc.Bottom-rc.Top)
}

func (f *fakeMenuDrawer) MenuBackground() (uint32, bool) { return 0x00123456, true }

// ownerDrawResult is what the child measured.
type ownerDrawResult struct {
	Error         string
	IDs           map[string]uint32 // item name -> id
	TypesOn       map[string]uint32 // fType of each item with the menu open, owner-drawn on
	TypesOff      map[string]uint32 // the same after Alpha was switched back
	TitleOff      string            // Alpha's text after that
	HiddenShown   bool              // the hidden item became visible
	Measured      map[uint32][2]int32
	Drawn         map[uint32]systray.Rect
	SepHeights    []int32
	SepMeasured   int
	Background    uint32 // the menu's background brush colour
	Areas         []string
	MeasuresFirst int      // Measure calls during the first open
	MeasuresAgain int      // during the second open
	GammaAgain    [2]int32 // Gamma measured and drawn high the second time
	DPIs          []uint32 // the DPIs Measure and Draw were given
	DeltaShown    bool     // Delta, hidden while the re-insert had it out, is in the open menu
	DeltaHide     string   // what became of that Hide
}

// openMenu posts the tray icon's right-click to the tray's window, as a
// click on the icon does, and returns the open menu's window and menu.
func openMenu() (windows.Handle, windows.Handle) {
	const wmUser, wmRButtonUp = 0x0400, 0x0205
	pODPostMessageW.Call(systray.MenuOwnerWindow(), wmUser+1, 0, wmRButtonUp)
	for range 100 {
		time.Sleep(50 * time.Millisecond)
		if w := ownMenuWindow(); w != 0 {
			const mnGetHMenu = 0x01E1
			m, _, _ := pODSendMessageW.Call(uintptr(w), mnGetHMenu, 0, 0)
			return w, windows.Handle(m)
		}
	}
	return 0, 0
}

// ownMenuWindow is a visible popup menu window (#32768) of this process.
func ownMenuWindow() windows.Handle {
	cls, _ := windows.UTF16PtrFromString("#32768")
	var prev uintptr
	for range 50 {
		h, _, _ := pODFindWindowExW.Call(0, prev, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var pid uint32
		pODGetWindowThreadID.Call(h, uintptr(unsafe.Pointer(&pid)))
		if v, _, _ := pODIsWindowVisible.Call(h); v != 0 && int(pid) == os.Getpid() {
			return windows.Handle(h)
		}
		prev = h
	}
	return 0
}

func closeMenu(w windows.Handle) {
	const wmKeyDown, vkEscape, wmCancelMode = 0x0100, 0x1B, 0x001F
	pODPostMessageW.Call(uintptr(w), wmKeyDown, vkEscape, 0)
	for range 40 {
		time.Sleep(50 * time.Millisecond)
		if ownMenuWindow() == 0 {
			return
		}
	}
	pODPostMessageW.Call(uintptr(w), wmCancelMode, 0, 0)
	pODPostMessageW.Call(systray.MenuOwnerWindow(), wmCancelMode, 0, 0)
	for range 40 {
		time.Sleep(50 * time.Millisecond)
		if ownMenuWindow() == 0 {
			return
		}
	}
}

// itemType is an item's fType (and its text) in menu m, by command id;
// ok false when the menu has no such item.
func itemType(m windows.Handle, id uint32) (uint32, string, bool) {
	const miimFType, miimString = 0x0100, 0x0040
	buf := make([]uint16, 64)
	mi := odMenuItemInfo{Mask: miimFType | miimString, TypeData: &buf[0], Cch: uint32(len(buf))}
	mi.Size = uint32(unsafe.Sizeof(mi))
	r, _, _ := pODGetMenuItemInfoW.Call(uintptr(m), uintptr(id), 0, uintptr(unsafe.Pointer(&mi)))
	if r == 0 {
		return 0, "", false
	}
	return mi.Type, windows.UTF16ToString(buf), true
}

// menuBackground is the colour of menu m's background brush (or 1<<31).
func menuBackground(m windows.Handle) uint32 {
	const mimBackground = 0x2
	mi := struct {
		Size, Mask, Style, Max uint32
		Background             windows.Handle
		ContextHelpID          uint32
		MenuData               uintptr
	}{Mask: mimBackground}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := pODGetMenuInfo.Call(uintptr(m), uintptr(unsafe.Pointer(&mi))); r == 0 || mi.Background == 0 {
		return 1 << 31
	}
	var lb struct {
		Style uint32
		Color uint32
		Hatch uintptr
	}
	if r, _, _ := pODGetObjectW.Call(uintptr(mi.Background), unsafe.Sizeof(lb), uintptr(unsafe.Pointer(&lb))); r == 0 {
		return 1 << 30
	}
	return lb.Color
}

// TestOwnerDrawChild is the child of TestOwnerDrawnItemIsOwnerDrawn: the
// patched tray library on this desktop with a fake drawer.
func TestOwnerDrawChild(t *testing.T) {
	if os.Getenv(ownerDrawChildEnv) == "" {
		t.Skip("child of TestOwnerDrawnItemIsOwnerDrawn")
	}
	runtime.LockOSThread()
	f := &fakeMenuDrawer{measured: map[uint32][2]int32{}, drawn: map[uint32]systray.Rect{}, dpis: map[uint32]bool{}}
	systray.SetMenuDrawer(f)
	res := ownerDrawResult{IDs: map[string]uint32{}, TypesOn: map[string]uint32{}, TypesOff: map[string]uint32{}}
	systray.Run(func() {
		defer systray.Quit()
		alpha := systray.AddMenuItem("Alpha", "")
		beta := systray.AddMenuItem("Beta", "")
		systray.AddOwnerDrawnSeparator()
		gamma := systray.AddMenuItemCheckbox("Gamma", "", true)
		hidden := systray.AddMenuItem("Hidden", "")
		hidden.Hide()
		alpha.SetOwnerDrawn(true)
		gamma.SetOwnerDrawn(true)
		hidden.SetOwnerDrawn(true) // stays hidden
		items := map[string]*systray.MenuItem{"alpha": alpha, "beta": beta, "gamma": gamma, "hidden": hidden}
		for k, it := range items {
			res.IDs[k] = it.ID()
		}
		w, m := openMenu()
		if w == 0 {
			res.Error = "no menu window"
			return
		}
		time.Sleep(500 * time.Millisecond)
		for k, it := range items {
			if ty, _, ok := itemType(m, it.ID()); ok {
				res.TypesOn[k] = ty
			}
		}
		_, _, res.HiddenShown = itemType(m, hidden.ID())
		res.Background = menuBackground(m)
		f.mu.Lock()
		res.MeasuresFirst = f.measures
		res.Measured = maps(f.measured)
		res.Drawn = maps(f.drawn)
		res.SepHeights = slices.Clone(f.seps)
		res.SepMeasured = f.sepMeas
		f.mu.Unlock()
		closeMenu(w)

		alpha.SetOwnerDrawn(false)
		f.mu.Lock()
		f.grow = 9 // its content grew: the next open asks again and draws it so
		f.drawn = map[uint32]systray.Rect{}
		f.mu.Unlock()
		w, m = openMenu()
		if w == 0 {
			res.Error = "no menu window the second time"
			return
		}
		time.Sleep(500 * time.Millisecond)
		for k, it := range items {
			if ty, s, ok := itemType(m, it.ID()); ok {
				res.TypesOff[k] = ty
				if k == "alpha" {
					res.TitleOff = s
				}
			}
		}
		f.mu.Lock()
		res.MeasuresAgain = f.measures - res.MeasuresFirst
		res.GammaAgain = [2]int32{f.measured[gamma.ID()][1], f.drawn[gamma.ID()].Bottom - f.drawn[gamma.ID()].Top}
		for dpi := range f.dpis {
			res.DPIs = append(res.DPIs, dpi)
		}
		f.mu.Unlock()
		closeMenu(w)

		// review A-m1: a Hide from another goroutine while the re-insert
		// before an open has the item out of the menu waits for it, and
		// the item stays hidden (without the lock it came back)
		delta := systray.AddMenuItem("Delta", "")
		delta.SetOwnerDrawn(true)
		hid := make(chan struct{})
		var once sync.Once
		systray.SetReinsertHookForTests(func(id uint32) {
			if id != delta.ID() {
				return
			}
			once.Do(func() {
				go func() { delta.Hide(); close(hid) }()
				time.Sleep(300 * time.Millisecond) // the Hide has all the time it needs
			})
		})
		w, m = openMenu()
		if w == 0 {
			res.Error = "no menu window the third time"
			return
		}
		select {
		case <-hid:
			res.DeltaHide = "done"
		case <-time.After(5 * time.Second):
			res.DeltaHide = "never returned"
		}
		time.Sleep(300 * time.Millisecond)
		_, _, res.DeltaShown = itemType(m, delta.ID())
		closeMenu(w)
		systray.SetReinsertHookForTests(nil)

		// what Windows sends every top-level window when the colour mode changes
		const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
		area, _ := windows.UTF16PtrFromString("ImmersiveColorSet")
		var r uintptr
		pODSendMessageTimeou.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(area)), smtoAbortIfHung, 3000, uintptr(unsafe.Pointer(&r)))
		time.Sleep(300 * time.Millisecond)
		f.mu.Lock()
		res.Areas = slices.Clone(f.areas)
		f.mu.Unlock()
	}, func() {
		b, _ := json.Marshal(res)
		os.Stdout.WriteString("\nownerdraw-result " + string(b) + "\n")
	})
}

func maps[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Plan Task 7: an item made owner-drawn is MFT_OWNERDRAW in the real menu
// and is measured and drawn by the drawer (at the size it measured); one
// switched back is a text item with its title again; a hidden item stays
// hidden; an owner-drawn separator is the drawer's too, and the menu's
// background is the drawer's colour; a settings change reaches the drawer.
func TestOwnerDrawnItemIsOwnerDrawn(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnerDrawChild$", "-test.v")
	cmd.Env = append(os.Environ(), ownerDrawChildEnv+"=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan *ownerDrawResult, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var got *ownerDrawResult
		for sc.Scan() {
			if s, ok := strings.CutPrefix(sc.Text(), "ownerdraw-result "); ok {
				got = &ownerDrawResult{}
				if err := json.Unmarshal([]byte(s), got); err != nil {
					got.Error = "result: " + err.Error()
				}
			}
		}
		done <- got
	}()
	var res *ownerDrawResult
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
	const mftOwnerDraw = 0x0100
	id := res.IDs
	eq(t, res.TypesOn, map[string]uint32{"alpha": mftOwnerDraw, "beta": 0, "gamma": mftOwnerDraw})
	eq(t, res.HiddenShown, false)
	// Measure for exactly the owner-drawn items; Draw at the measured height
	got := []uint32{}
	for k := range res.Measured {
		got = append(got, k)
	}
	slices.Sort(got)
	eq(t, got, []uint32{id["alpha"], id["gamma"]})
	for _, k := range []string{"alpha", "gamma"} {
		rc, ok := res.Drawn[id[k]]
		if !ok {
			t.Fatalf("%s was not drawn", k)
		}
		if h := rc.Bottom - rc.Top; h != res.Measured[id[k]][1] {
			t.Fatalf("%s drawn %d high, measured %d", k, h, res.Measured[id[k]][1])
		}
		if w := rc.Right - rc.Left; w < res.Measured[id[k]][0] {
			t.Fatalf("%s drawn %d wide, measured %d", k, w, res.Measured[id[k]][0])
		}
	}
	if res.SepMeasured == 0 || !slices.Equal(res.SepHeights, []int32{13}) {
		t.Fatalf("separator measured %d times, drawn %v", res.SepMeasured, res.SepHeights)
	}
	eq(t, res.Background, uint32(0x00123456))
	eq(t, res.TypesOff, map[string]uint32{"alpha": 0, "beta": 0, "gamma": mftOwnerDraw})
	eq(t, res.TitleOff, "Alpha")
	if res.MeasuresAgain == 0 {
		t.Fatal("the second open measured nothing again")
	}
	if g := res.GammaAgain; g[0] != res.Measured[id["gamma"]][1]+9 || g[1] != g[0] {
		t.Fatalf("Gamma the second time: measured %d high, drawn %d (first %d)", g[0], g[1], res.Measured[id["gamma"]][1])
	}
	if !slices.Contains(res.Areas, "ImmersiveColorSet") {
		t.Fatalf("settings changes seen: %q", res.Areas)
	}
	// review B-M8: measured and drawn at the runner's DPI (100 %)
	eq(t, res.DPIs, []uint32{96})
	// review A-m1
	eq(t, res.DeltaHide, "done")
	if res.DeltaShown {
		t.Fatal("Delta, hidden during the re-insert, is in the menu")
	}
	t.Logf("measures: first open %d, second %d; separator drawn %v", res.MeasuresFirst, res.MeasuresAgain, res.SepHeights)
}
