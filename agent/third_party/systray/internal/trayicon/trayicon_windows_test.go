//go:build windows

// Added to the upstream v1.12.2 code; see PATCHED.md.

package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32         = windows.NewLazySystemDLL("Gdi32.dll")
	pGetIconInfo  = user32.NewProc("GetIconInfo")
	pDestroyIcon  = user32.NewProc("DestroyIcon")
	pGetDC        = user32.NewProc("GetDC")
	pReleaseDC    = user32.NewProc("ReleaseDC")
	pGetObject    = gdi32.NewProc("GetObjectW")
	pGetDIBits    = gdi32.NewProc("GetDIBits")
	pDeleteObject = gdi32.NewProc("DeleteObject")
)

// testICO is an ICO with one PNG entry per size, each a solid colour whose
// red channel is the size.
func testICO(t *testing.T, sizes []int) []byte {
	t.Helper()
	var entries [][]byte
	for _, n := range sizes {
		img := image.NewNRGBA(image.Rect(0, 0, n, n))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(n), 0x40, 0x80, 0xff
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, b.Bytes())
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, n := range sizes {
		_ = binary.Write(&b, le, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{uint8(n), uint8(n), 0, 0, 1, 32, uint32(len(entries[i])), uint32(off)})
		off += len(entries[i])
	}
	for _, e := range entries {
		b.Write(e)
	}
	return b.Bytes()
}

// iconPixels returns the size and the top-down BGRA pixels of an icon's
// colour bitmap.
func iconPixels(t *testing.T, h windows.Handle) (int, int, []byte) {
	t.Helper()
	var ii struct {
		FIcon              int32
		XHotspot, YHotspot uint32
		HbmMask, HbmColor  uintptr
	}
	if r, _, err := pGetIconInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&ii))); r == 0 {
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
	w, hgt := int(bm.Width), int(bm.Height)
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
	bi.Width, bi.Height, bi.Planes, bi.BitCount = int32(w), -int32(hgt), 1, 32
	pix := make([]byte, w*hgt*4)
	dc, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, dc)
	if r, _, err := pGetDIBits.Call(dc, ii.HbmColor, 0, uintptr(hgt), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		t.Fatalf("GetDIBits: %v", err)
	}
	return w, hgt, pix
}

// The tray (systray_windows.go loadIconFrom) loads its icon with
// Load(path, Size()): Windows takes the ICO entry of exactly the size the
// notification area draws, instead of the large-icon entry that the shell
// would then scale down (upstream's LR_DEFAULTSIZE).
func TestLoadTakesTheSmallEntry(t *testing.T) {
	cx, cy := Size()
	small, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	large, _, _ := pGetSystemMetrics.Call(11) // SM_CXICON
	t.Logf("Size %dx%d, SM_CXSMICON %d, SM_CXICON %d", cx, cy, small, large)
	if cx != cy || cx < 16 {
		t.Fatalf("Size %dx%d", cx, cy)
	}
	// a test binary has no DPI-awareness manifest: both are at 96 DPI
	if int(cx) != int(small) {
		t.Fatalf("Size %d, small-icon metric %d", cx, small)
	}
	if int(cx) == int(large) {
		t.Fatalf("small and large icon sizes are both %d: the test cannot tell them apart", cx)
	}
	sizes := []int{16, 20, 24, 28, 32, 36, 40, 48, 56, 64}
	path := filepath.Join(t.TempDir(), "tray.ico")
	if err := os.WriteFile(path, testICO(t, sizes), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Load(path, cx, cy)
	if err != nil {
		t.Fatal(err)
	}
	defer pDestroyIcon.Call(uintptr(h))
	w, hgt, pix := iconPixels(t, h)
	if w != int(cx) || hgt != int(cy) {
		t.Fatalf("loaded icon is %dx%d, want %dx%d", w, hgt, cx, cy)
	}
	// every pixel is the colour of the entry of exactly that size (BGRA)
	want := color.NRGBA{byte(cx), 0x40, 0x80, 0xff}
	for i := 0; i < len(pix); i += 4 {
		if got := (color.NRGBA{pix[i+2], pix[i+1], pix[i], pix[i+3]}); got != want {
			t.Fatalf("pixel %d is %v, want %v (the %d px entry)", i/4, got, want, cx)
		}
	}
}
