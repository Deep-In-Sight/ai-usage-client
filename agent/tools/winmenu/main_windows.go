package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pFindWindowExW                 = user32.NewProc("FindWindowExW")
	pGetWindowThreadProcessID      = user32.NewProc("GetWindowThreadProcessId")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pSendMessageW                  = user32.NewProc("SendMessageW")
	pSendMessageTimeoutW           = user32.NewProc("SendMessageTimeoutW")
	pGetMenuItemCount              = user32.NewProc("GetMenuItemCount")
	pGetMenuItemRect               = user32.NewProc("GetMenuItemRect")
	pGetMenuItemInfoW              = user32.NewProc("GetMenuItemInfoW")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pSetCursorPos                  = user32.NewProc("SetCursorPos")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pGetDC                         = user32.NewProc("GetDC")
	pReleaseDC                     = user32.NewProc("ReleaseDC")
	pGetDisplayConfigBufferSizes   = user32.NewProc("GetDisplayConfigBufferSizes")
	pQueryDisplayConfig            = user32.NewProc("QueryDisplayConfig")
	pDisplayConfigGetDeviceInfo    = user32.NewProc("DisplayConfigGetDeviceInfo")
	pDisplayConfigSetDeviceInfo    = user32.NewProc("DisplayConfigSetDeviceInfo")
	pEnumDisplaySettingsW          = user32.NewProc("EnumDisplaySettingsW")
	pChangeDisplaySettingsExW      = user32.NewProc("ChangeDisplaySettingsExW")
	pMonitorFromPoint              = user32.NewProc("MonitorFromPoint")
	pGetDpiForMonitor              = windows.NewLazySystemDLL("shcore.dll").NewProc("GetDpiForMonitor")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pBitBlt             = gdi32.NewProc("BitBlt")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
)

type rect struct{ Left, Top, Right, Bottom int32 }

func main() {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_TEMP") == "" {
		fmt.Fprintln(os.Stderr, "winmenu: refusing to run outside GitHub Actions (GITHUB_ACTIONS=true, RUNNER_TEMP set): it changes the display scale")
		os.Exit(2)
	}
	pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2: physical pixels
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: winmenu open|broadcast|scale ...")
		os.Exit(2)
	}
	var out any
	var err error
	switch os.Args[1] {
	case "open":
		fs := flag.NewFlagSet("open", flag.ExitOnError)
		pid := fs.Int("pid", 0, "the tray helper's process")
		pngPath := fs.String("png", "", "the menu's picture")
		_ = fs.Parse(os.Args[2:])
		out, err = open(uint32(*pid), *pngPath)
	case "broadcast":
		out, err = map[string]any{"broadcast": broadcast()}, nil
	case "scale":
		fs := flag.NewFlagSet("scale", flag.ExitOnError)
		to := fs.String("to", "", "125, 150 or restore")
		state := fs.String("state", "", "where the display's state is kept for restore")
		_ = fs.Parse(os.Args[2:])
		out, err = scale(*to, *state)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		out = map[string]any{"error": err.Error()}
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	if err != nil {
		os.Exit(1)
	}
}

func utf16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

// windowOf is a top-level window of class cls that process pid owns (and
// is visible, if visible).
func windowOf(pid uint32, cls string, visible bool) windows.Handle {
	var prev uintptr
	for range 200 {
		h, _, _ := pFindWindowExW.Call(0, prev, uintptr(unsafe.Pointer(utf16(cls))), 0)
		if h == 0 {
			return 0
		}
		prev = h
		var p uint32
		pGetWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&p)))
		if p != pid {
			continue
		}
		if v, _, _ := pIsWindowVisible.Call(h); visible && v == 0 {
			continue
		}
		return windows.Handle(h)
	}
	return 0
}

// openResult is what open measured.
type openResult struct {
	Menu      [2]int32 `json:"menu"` // the menu window's width and height
	DPI       uint32   `json:"dpi"`
	Items     []item   `json:"items"`
	Header    int      `json:"header"` // index of the first header
	Row       int      `json:"row"`
	Quit      int      `json:"quit"`
	Frame     string   `json:"frame"`      // the menu window's outermost pixel (left, half-way down)
	Frame2    string   `json:"frame2"`     // one pixel further in
	Pad       string   `json:"pad"`        // just left of the items
	PadTop    string   `json:"pad_top"`    // just above the first item
	HeaderBg  string   `json:"header_bg"`  // inside the header, top right
	QuitBg    string   `json:"quit_bg"`    // inside Quit, top right
	Lum       float64  `json:"lum"`        // the header background's luminance
	BgDiff    int      `json:"bg_diff"`    // header vs Quit
	HoverRow  string   `json:"hover_row"`  // the first row, the pointer on it
	HoverQuit string   `json:"hover_quit"` // Quit, the pointer on it
	HoverDiff int      `json:"hover_diff"`
	Picture   string   `json:"picture"`
	// verdict numbers: items whose height the panel does not draw at this
	// DPI (and items not owner-drawn); the header's drawn width minus the
	// measured one (Windows' gutter); the header background's distance to
	// the dark set's; the padding's distance to the header background (0:
	// everything inside the frame is ours); the hovered row's to the
	// background
	HeightProblems []string `json:"height_problems"`
	Gutter         int32    `json:"gutter"`
	DarkDiff       int      `json:"dark_diff"`
	PadDiff        int      `json:"pad_diff"`
	HoverVsBg      int      `json:"hover_vs_bg"`
}

func open(pid uint32, pngPath string) (*openResult, error) {
	owner := windowOf(pid, "SystrayClass", false)
	if owner == 0 {
		return nil, fmt.Errorf("process %d has no SystrayClass window", pid)
	}
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	// the menu opens up from the pointer: put it where nothing is under it
	pSetCursorPos.Call(sw/2, sh-2)
	time.Sleep(300 * time.Millisecond)
	const wmUser, wmRButtonUp = 0x0400, 0x0205
	pPostMessageW.Call(uintptr(owner), wmUser+1, 0, wmRButtonUp)
	var mw windows.Handle
	for range 100 {
		time.Sleep(100 * time.Millisecond)
		if mw = windowOf(pid, "#32768", true); mw != 0 {
			break
		}
	}
	if mw == 0 {
		return nil, errors.New("no menu window within 10 s")
	}
	defer closeMenu(pid, mw)
	time.Sleep(800 * time.Millisecond)
	res := &openResult{}
	var wr rect
	pGetWindowRect.Call(uintptr(mw), uintptr(unsafe.Pointer(&wr)))
	res.Menu = [2]int32{wr.Right - wr.Left, wr.Bottom - wr.Top}
	d, _, _ := pGetDpiForWindow.Call(uintptr(mw))
	res.DPI = uint32(d)
	const mnGetHMenu = 0x01E1
	menu, _, _ := pSendMessageW.Call(uintptr(mw), mnGetHMenu, 0, 0)
	if menu == 0 {
		return res, errors.New("MN_GETHMENU answered nothing")
	}
	n, _, _ := pGetMenuItemCount.Call(menu)
	for i := range int(int32(n)) {
		var r rect
		pGetMenuItemRect.Call(0, menu, uintptr(i), uintptr(unsafe.Pointer(&r)))
		const miimFType, miimState = 0x0100, 0x0001
		mi := struct {
			Size, Mask, Type, State     uint32
			ID                          uint32
			SubMenu, Checked, Unchecked windows.Handle
			ItemData                    uintptr
			TypeData                    *uint16
			Cch                         uint32
			BMPItem                     windows.Handle
		}{Mask: miimFType | miimState}
		mi.Size = uint32(unsafe.Sizeof(mi))
		pGetMenuItemInfoW.Call(menu, uintptr(i), 1, uintptr(unsafe.Pointer(&mi)))
		const mftOwnerDraw, mftSeparator, mfsDisabled = 0x0100, 0x0800, 0x0003
		res.Items = append(res.Items, item{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Bottom,
			Owner: mi.Type&mftOwnerDraw != 0, Sep: mi.Type&mftSeparator != 0, Disabled: mi.State&mfsDisabled != 0})
	}
	var err error
	if res.Header, res.Row, res.Quit, err = roles(res.Items); err != nil {
		return res, err
	}
	m := panel.MetricsAt(int(res.DPI))
	res.HeightProblems = append([]string{}, heightProblems(res.Items, m)...) // [] rather than null
	res.Gutter = res.Items[res.Header].w() - int32(m.Width)
	shot := capture()
	hd, qt, rw := res.Items[res.Header], res.Items[res.Quit], res.Items[res.Row]
	mid := (wr.Top + wr.Bottom) / 2
	res.Frame, res.Frame2 = shot.at(wr.Left, mid).String(), shot.at(wr.Left+1, mid).String()
	pad, padTop := shot.at(hd.Left-1, (hd.Top+hd.Bottom)/2), shot.at((hd.Left+hd.Right)/2, res.Items[0].Top-1)
	res.Pad, res.PadTop = pad.String(), padTop.String()
	hb, qb := shot.at(hd.Right-3, hd.Top+2), shot.at(qt.Right-3, qt.Top+2)
	res.HeaderBg, res.QuitBg, res.Lum, res.BgDiff = hb.String(), qb.String(), lum(hb), diff(hb, qb)
	dark := panel.DarkPalette.Background
	res.DarkDiff = diff(hb, rgb{dark.R, dark.G, dark.B})
	res.PadDiff = max(diff(pad, hb), diff(padTop, hb))
	if pngPath != "" {
		if err := shot.save(pngPath, wr); err != nil {
			return res, err
		}
		res.Picture = pngPath
	}
	// one hover rule: the first row and Quit under the pointer
	hover := func(it item) rgb {
		pSetCursorPos.Call(uintptr((it.Left+it.Right)/2), uintptr((it.Top+it.Bottom)/2))
		time.Sleep(700 * time.Millisecond)
		return capture().at(it.Right-3, it.Top+2)
	}
	hr, hq := hover(rw), hover(qt)
	res.HoverRow, res.HoverQuit, res.HoverDiff, res.HoverVsBg = hr.String(), hq.String(), diff(hr, hq), diff(hr, hb)
	pSetCursorPos.Call(sw/2, sh-2)
	return res, nil
}

// closeMenu ends the menu (Escape, else WM_CANCELMODE).
func closeMenu(pid uint32, mw windows.Handle) {
	const wmKeyDown, vkEscape, wmCancelMode = 0x0100, 0x1B, 0x001F
	pPostMessageW.Call(uintptr(mw), wmKeyDown, vkEscape, 0)
	for range 30 {
		time.Sleep(100 * time.Millisecond)
		if windowOf(pid, "#32768", true) == 0 {
			return
		}
	}
	pPostMessageW.Call(uintptr(mw), wmCancelMode, 0, 0)
	time.Sleep(time.Second)
}

func broadcast() bool {
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	var r uintptr
	ok, _, _ := pSendMessageTimeoutW.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(utf16("ImmersiveColorSet"))),
		smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&r)))
	return ok != 0
}

// shot is the screen's pixels.
type shot struct {
	px   []byte
	w, h int32
}

func capture() shot {
	w, _, _ := pGetSystemMetrics.Call(0)
	h, _, _ := pGetSystemMetrics.Call(1)
	sdc, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, sdc)
	mdc, _, _ := pCreateCompatibleDC.Call(sdc)
	defer pDeleteDC.Call(mdc)
	bi := struct {
		Size                         uint32
		Width, Height                int32
		Planes, BitCount             uint16
		Compression, SizeImage       uint32
		XPelsPerMeter, YPelsPerMeter int32
		ClrUsed, ClrImportant        uint32
	}{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	bmp, _, _ := pCreateDIBSection.Call(sdc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return shot{}
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mdc, bmp)
	const srcCopy, captureBlt = 0x00CC0020, 0x40000000
	pBitBlt.Call(mdc, 0, 0, w, h, sdc, 0, 0, srcCopy|captureBlt)
	pSelectObject.Call(mdc, old)
	px := make([]byte, int(w)*int(h)*4)
	copy(px, unsafe.Slice((*byte)(bits), len(px)))
	return shot{px, int32(w), int32(h)}
}

func (s shot) at(x, y int32) rgb {
	if x < 0 || y < 0 || x >= s.w || y >= s.h || s.px == nil {
		return rgb{}
	}
	i := (int(y)*int(s.w) + int(x)) * 4
	return rgb{s.px[i+2], s.px[i+1], s.px[i]}
}

func (s shot) save(path string, r rect) error {
	r = rect{r.Left - 8, r.Top - 8, r.Right + 8, r.Bottom + 8}
	img := image.NewNRGBA(image.Rect(0, 0, int(r.Right-r.Left), int(r.Bottom-r.Top)))
	for y := r.Top; y < r.Bottom; y++ {
		for x := r.Left; x < r.Right; x++ {
			c := s.at(x, y)
			img.SetNRGBA(int(x-r.Left), int(y-r.Top), color.NRGBA{c.R, c.G, c.B, 0xff})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// ---- display scale (DisplayConfig's undocumented scale types -3 and -4,
// as stage 0's probe) and the screen mode

type dispState struct {
	AdapterLow  uint32 `json:"adapter_low"`
	AdapterHigh int32  `json:"adapter_high"`
	SourceID    uint32 `json:"source_id"`
	OrigRel     int32  `json:"orig_rel"`
	ModeChanged bool   `json:"mode_changed"`
}

var scales = []int{100, 125, 150, 175, 200, 225, 250, 300, 350, 400, 450, 500}

func primarySource() (uint32, int32, uint32, error) {
	var np, nm uint32
	const qdcOnlyActivePaths = 2
	if r, _, _ := pGetDisplayConfigBufferSizes.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 {
		return 0, 0, 0, fmt.Errorf("GetDisplayConfigBufferSizes %d", r)
	}
	if np == 0 {
		return 0, 0, 0, errors.New("no display paths")
	}
	paths := make([]byte, 72*np)
	modes := make([]byte, 64*(nm+1))
	if r, _, _ := pQueryDisplayConfig.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])), uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return 0, 0, 0, fmt.Errorf("QueryDisplayConfig %d", r)
	}
	return *(*uint32)(unsafe.Pointer(&paths[0])), *(*int32)(unsafe.Pointer(&paths[4])), *(*uint32)(unsafe.Pointer(&paths[8])), nil
}

type dpiGet struct {
	Type          int32
	Size          uint32
	AdapterLow    uint32
	AdapterHigh   int32
	ID            uint32
	Min, Cur, Max int32
}

type dpiSet struct {
	Type        int32
	Size        uint32
	AdapterLow  uint32
	AdapterHigh int32
	ID          uint32
	Rel         int32
}

func getScale(low uint32, high int32, id uint32) (dpiGet, error) {
	g := dpiGet{Type: -3, AdapterLow: low, AdapterHigh: high, ID: id}
	g.Size = uint32(unsafe.Sizeof(g))
	if r, _, _ := pDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&g))); r != 0 {
		return g, fmt.Errorf("get %d", r)
	}
	return g, nil
}

func setRel(low uint32, high int32, id uint32, rel int32) error {
	s := dpiSet{Type: -4, AdapterLow: low, AdapterHigh: high, ID: id, Rel: rel}
	s.Size = uint32(unsafe.Sizeof(s))
	if r, _, _ := pDisplayConfigSetDeviceInfo.Call(uintptr(unsafe.Pointer(&s))); r != 0 {
		return fmt.Errorf("set %d", r)
	}
	return nil
}

func screenMode() (uint32, uint32) {
	dm := make([]byte, 220)
	*(*uint16)(unsafe.Pointer(&dm[68])) = 220
	if r, _, _ := pEnumDisplaySettingsW.Call(0, ^uintptr(0), uintptr(unsafe.Pointer(&dm[0]))); r == 0 {
		return 0, 0
	}
	return *(*uint32)(unsafe.Pointer(&dm[172])), *(*uint32)(unsafe.Pointer(&dm[176]))
}

// setMode changes the screen mode for now only (not saved).
func setMode(w, h uint32) int32 {
	dm := make([]byte, 220)
	*(*uint16)(unsafe.Pointer(&dm[68])) = 220
	pEnumDisplaySettingsW.Call(0, ^uintptr(0), uintptr(unsafe.Pointer(&dm[0])))
	*(*uint32)(unsafe.Pointer(&dm[72])) = 0x80000 | 0x100000 // DM_PELSWIDTH | DM_PELSHEIGHT
	*(*uint32)(unsafe.Pointer(&dm[172])) = w
	*(*uint32)(unsafe.Pointer(&dm[176])) = h
	r, _, _ := pChangeDisplaySettingsExW.Call(0, uintptr(unsafe.Pointer(&dm[0])), 0, 0, 0)
	return int32(r)
}

func primaryDPI() uint32 {
	mon, _, _ := pMonitorFromPoint.Call(0, 1) // MONITOR_DEFAULTTOPRIMARY
	var x, y uint32
	if hr, _, _ := pGetDpiForMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y))); hr != 0 {
		return 0
	}
	return x
}

type scaleResult struct {
	DPI     uint32 `json:"dpi"`
	Screen  string `json:"screen"`
	Mode    string `json:"mode"`
	Rel     int32  `json:"rel"`
	Range   string `json:"range"`
	Restore bool   `json:"restore"`
}

func scale(to, statePath string) (*scaleResult, error) {
	if statePath == "" {
		return nil, errors.New("no -state")
	}
	low, high, id, err := primarySource()
	if err != nil {
		return nil, err
	}
	if to == "restore" {
		b, err := os.ReadFile(statePath)
		if err != nil {
			return &scaleResult{Restore: true, DPI: primaryDPI()}, nil // nothing was changed
		}
		var st dispState
		if err := json.Unmarshal(b, &st); err != nil {
			return nil, err
		}
		e := setRel(st.AdapterLow, st.AdapterHigh, st.SourceID, st.OrigRel)
		mode := "kept"
		if st.ModeChanged {
			r, _, _ := pChangeDisplaySettingsExW.Call(0, 0, 0, 0, 0) // back to the saved mode
			mode = fmt.Sprint(int32(r))
		}
		time.Sleep(2 * time.Second)
		w, h := screenMode()
		_ = os.Remove(statePath)
		return &scaleResult{Restore: true, DPI: primaryDPI(), Screen: fmt.Sprintf("%dx%d", w, h), Mode: mode, Rel: st.OrigRel}, e
	}
	target := 0
	fmt.Sscan(to, &target)
	ti := -1
	for i, s := range scales {
		if s == target {
			ti = i
		}
	}
	if ti < 0 {
		return nil, fmt.Errorf("scale %q", to)
	}
	g, err := getScale(low, high, id)
	if err != nil {
		return nil, err
	}
	st := dispState{AdapterLow: low, AdapterHigh: high, SourceID: id, OrigRel: g.Cur}
	if b, err := os.ReadFile(statePath); err == nil { // an earlier change: keep the original
		var old dispState
		if json.Unmarshal(b, &old) == nil {
			st.OrigRel, st.ModeChanged = old.OrigRel, old.ModeChanged
		}
	}
	rel := int32(ti + int(g.Min)) // Min is minus the recommended scale's index
	mode := "kept"
	if rel > g.Max {
		// the screen is too small for this scale: a larger mode, for now only
		st.ModeChanged = true
		b, _ := json.Marshal(st)
		_ = os.WriteFile(statePath, b, 0o644)
		mode = fmt.Sprint(setMode(1920, 1080))
		time.Sleep(2 * time.Second)
		if g, err = getScale(low, high, id); err != nil {
			return nil, err
		}
		rel = int32(ti + int(g.Min))
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(statePath, b, 0o644); err != nil {
		return nil, err
	}
	if err := setRel(low, high, id, rel); err != nil {
		return nil, err
	}
	var dpi uint32
	for range 20 {
		time.Sleep(500 * time.Millisecond)
		if dpi = primaryDPI(); dpi == uint32(target*96/100) {
			break
		}
	}
	w, h := screenMode()
	return &scaleResult{DPI: dpi, Screen: fmt.Sprintf("%dx%d", w, h), Mode: mode, Rel: rel, Range: fmt.Sprintf("%d..%d", g.Min, g.Max)}, nil
}
