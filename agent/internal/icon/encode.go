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

// TrayImage is the tray icon file for goos: an ICO with the number for
// Windows, a 48 px PNG with the number for Linux, and a 36 px PNG without it
// for macOS (which shows the number as text next to the icon).
func TrayImage(goos string, s Spec) []byte {
	switch goos {
	case "windows":
		s.Number = true
		var imgs []*image.NRGBA
		for _, n := range []int{16, 20, 24, 32, 48} {
			imgs = append(imgs, Ring(s, n))
		}
		return ICO(imgs...)
	case "darwin":
		s.Number = false
		return PNG(Ring(s, 36))
	}
	s.Number = true
	return PNG(Ring(s, 48))
}
