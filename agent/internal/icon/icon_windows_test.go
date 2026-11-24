package icon

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	gdi32             = windows.NewLazySystemDLL("gdi32.dll")
	pGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	pLoadImage        = user32.NewProc("LoadImageW")
	pGetIconInfo      = user32.NewProc("GetIconInfo")
	pDestroyIcon      = user32.NewProc("DestroyIcon")
	pGetDC            = user32.NewProc("GetDC")
	pReleaseDC        = user32.NewProc("ReleaseDC")
	pGetObject        = gdi32.NewProc("GetObjectW")
	pGetDIBits        = gdi32.NewProc("GetDIBits")
	pDeleteObject     = gdi32.NewProc("DeleteObject")
)

// loadTrayICO loads an ICO file as the patched systray does for the tray
// (third_party/systray: LoadImage with LR_LOADFROMFILE at the small-icon
// size; TestTrayIconLoadsTheSmallEntry there checks that path) and returns
// the loaded image's size and straight-alpha NRGBA pixels.
func loadTrayICO(t *testing.T, path string, n int) (int, []color.NRGBA) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	must(t, err)
	const imageIcon, lrLoadFromFile = 1, 0x10
	h, _, err := pLoadImage.Call(0, uintptr(unsafe.Pointer(p)), imageIcon, uintptr(n), uintptr(n), lrLoadFromFile)
	if h == 0 {
		t.Fatalf("LoadImage: %v", err)
	}
	defer pDestroyIcon.Call(h)
	var ii struct {
		FIcon              int32
		XHotspot, YHotspot uint32
		HbmMask, HbmColor  uintptr
	}
	if r, _, err := pGetIconInfo.Call(h, uintptr(unsafe.Pointer(&ii))); r == 0 {
		t.Fatalf("GetIconInfo: %v", err)
	}
	defer pDeleteObject.Call(ii.HbmMask)
	defer pDeleteObject.Call(ii.HbmColor)
	var bm struct {
		Type, Width, Height, WidthBytes int32
		Planes, BitsPixel               uint16
		Bits                            uintptr
	}
	if r, _, err := pGetObject.Call(ii.HbmColor, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		t.Fatalf("GetObject: %v", err)
	}
	w := int(bm.Width)
	if int(bm.Height) != w {
		t.Fatalf("loaded icon is %dx%d", w, bm.Height)
	}
	var bi struct {
		Size                   uint32
		Width, Height          int32
		Planes, BitCount       uint16
		Compression, SizeImage uint32
		XPels, YPels           int32
		ClrUsed, ClrImportant  uint32
		Colors                 [4]uint32
	}
	bi.Size = 40
	bi.Width, bi.Height, bi.Planes, bi.BitCount = int32(w), -int32(w), 1, 32
	buf := make([]byte, w*w*4)
	dc, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, dc)
	if r, _, err := pGetDIBits.Call(dc, ii.HbmColor, 0, uintptr(w), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		t.Fatalf("GetDIBits: %v", err)
	}
	px := make([]color.NRGBA, w*w)
	for i := range px {
		px[i] = color.NRGBA{buf[4*i+2], buf[4*i+1], buf[4*i], buf[4*i+3]}
	}
	return w, px
}

// What Windows loads for the tray at this runner's DPI is the layout drawn
// for exactly that size: no rescaling.
func TestWindowsLoadsTheTrayLayoutForItsSize(t *testing.T) {
	r, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	n := int(r)
	t.Logf("small-icon size %d", n)
	stale := clawdSpec(p(71), usage.LevelNormal)
	stale.Stale = true
	for _, s := range []Spec{
		clawdSpec(p(42), usage.LevelNormal), clawdSpec(p(80), usage.LevelWarning),
		clawdSpec(p(100), usage.LevelCritical), clawdSpec(nil, usage.LevelNone), stale,
		{Level: usage.LevelNone}, // signed out: the ring
	} {
		path := filepath.Join(t.TempDir(), "tray.ico")
		must(t, os.WriteFile(path, TrayImage("windows", s), 0o600))
		w, got := loadTrayICO(t, path, n)
		if w != n {
			t.Fatalf("%+v: loaded %d px, want %d", s, w, n)
		}
		withNumber := s
		withNumber.Number = true
		want := Tray(withNumber, n)
		for i, g := range got {
			x, y := i%n, i/n
			wc := want.NRGBAAt(x, y)
			if wc.A == 0 && g.A == 0 {
				continue
			}
			if wc.A == 0xff && g != wc || wc.A != g.A {
				t.Fatalf("%+v: pixel %d,%d is %v, drawn %v", s, x, y, g, wc)
			}
		}
	}
}
