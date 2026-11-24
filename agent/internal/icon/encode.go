package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

// PNG encodes img.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err) // only fails on a write error, and bytes.Buffer does not fail
	}
	return b.Bytes()
}

// ICO packs the images into one ICO file with PNG-compressed entries.
func ICO(imgs ...*image.NRGBA) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, [3]uint16{0, 1, uint16(len(imgs))})
	data := make([][]byte, len(imgs))
	off := 6 + 16*len(imgs)
	for i, img := range imgs {
		data[i] = PNG(img)
		dim := func(n int) uint8 {
			if n >= 256 {
				return 0
			}
			return uint8(n)
		}
		_ = binary.Write(&b, le, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim(img.Bounds().Dx()), dim(img.Bounds().Dy()), 0, 0, 1, 32, uint32(len(data[i])), uint32(off)})
		off += len(data[i])
	}
	for _, d := range data {
		b.Write(d)
	}
	return b.Bytes()
}

// TrayIconSizes are the Windows ICO's entries: the notification area's
// icon size (SM_CXSMICON) at 100 % to 400 % display scale. The tray loads
// the entry of exactly that size (third_party/systray, trayicon).
var TrayIconSizes = []int{16, 20, 24, 28, 32, 36, 40, 48, 56, 64}

// TrayImage is the tray icon file for goos (see Tray): an ICO of
// TrayIconSizes with the number for Windows, which shows no text next to
// the icon;
// a 32 px PNG without the number for macOS (16 pt at 2x; the menu bar
// title shows it); a 32 px PNG for Linux, with the number only when
// s.Number says so (the caller knows whether the desktop shows the label).
func TrayImage(goos string, s Spec) []byte {
	switch goos {
	case "windows":
		s.Number = true
		var imgs []*image.NRGBA
		for _, n := range TrayIconSizes {
			imgs = append(imgs, Tray(s, n))
		}
		return ICO(imgs...)
	case "darwin":
		s.Number = false
	}
	return PNG(Tray(s, 32))
}
