package icon

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

// The menu's provider header shows the provider's artwork in 16 px (32 px
// for the Linux and macOS hosts, which scale it to 16 px, sharp on HiDPI);
// Windows gets an ICO with the notification area's sizes. A provider
// without artwork gets no picture.
func TestMenuImage(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		b := MenuImage(goos, Claude)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
			t.Fatalf("%s: %v", goos, img.Bounds())
		}
		// Clawd at twice its grid (22×16), centred on whole cells, nothing else
		ink, clear := 0, 0
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				r, g, bl, a := img.At(x, y).RGBA()
				switch {
				case a == 0:
					clear++
				case r>>8 == 0xd9 && g>>8 == 0x77 && bl>>8 == 0x57:
					ink++
				default:
					t.Fatalf("%s: pixel %d,%d is neither Clawd nor clear", goos, x, y)
				}
			}
		}
		cells := 0
		for _, row := range arts[Claude].Rows {
			for _, c := range row {
				if c == '#' {
					cells++
				}
			}
		}
		if ink != cells*4 {
			t.Fatalf("%s: %d ink pixels, want %d", goos, ink, cells*4)
		}
	}
	ico := MenuImage("windows", Claude)
	var hdr [3]uint16
	if err := binary.Read(bytes.NewReader(ico), binary.LittleEndian, &hdr); err != nil || hdr[1] != 1 || int(hdr[2]) != len(TrayIconSizes) {
		t.Fatalf("ICO header %v %v", hdr, err)
	}
	for _, p := range []Provider{"", "codex", "other"} {
		if MenuImage("linux", p) != nil || MenuImage("windows", p) != nil {
			t.Fatalf("a picture for %q", p)
		}
	}
}
