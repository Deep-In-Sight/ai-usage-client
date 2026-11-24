package icon

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

func p(v float64) *float64 { return &v }

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

func TestRingFraction(t *testing.T) {
	img := Ring(Spec{Percent: p(50), Level: usage.LevelNormal}, 32)
	eq(t, img.NRGBAAt(29, 16), Normal)     // 3 o'clock, inside the stroke: filled
	eq(t, img.NRGBAAt(2, 16), Track)       // 9 o'clock: track only
	eq(t, img.NRGBAAt(16, 16).A, uint8(0)) // centre stays transparent without a number
	eq(t, img.NRGBAAt(0, 0).A, uint8(0))   // corner outside the ring
}

func TestRingLevelColours(t *testing.T) {
	eq(t, Ring(Spec{Percent: p(80), Level: usage.LevelWarning}, 32).NRGBAAt(22, 5), Warning)
	eq(t, Ring(Spec{Percent: p(95), Level: usage.LevelCritical}, 32).NRGBAAt(22, 5), Critical)
	// the level, not the number, picks the colour (a flagged 40%)
	eq(t, Ring(Spec{Percent: p(40), Level: usage.LevelWarning}, 32).NRGBAAt(22, 5), Warning)
}

func TestRingNoData(t *testing.T) {
	for _, s := range []Spec{{Level: usage.LevelNone}, {Level: usage.LevelNone, Number: true}} {
		img := Ring(s, 32)
		track := 0
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				c := img.NRGBAAt(x, y)
				if c == Normal || c == Warning || c == Critical {
					t.Fatalf("level colour at %d,%d", x, y)
				}
				if c == Track {
					track++
				}
			}
		}
		if track == 0 {
			t.Fatal("no ring drawn")
		}
	}
}

func TestRingZeroAndFull(t *testing.T) {
	zero := Ring(Spec{Percent: p(0), Level: usage.LevelNormal}, 32)
	full := Ring(Spec{Percent: p(100), Level: usage.LevelCritical}, 32)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if zero.NRGBAAt(x, y) == Normal {
				t.Fatalf("0%% fills %d,%d", x, y)
			}
			if full.NRGBAAt(x, y) == Track {
				t.Fatalf("100%% leaves track at %d,%d", x, y)
			}
		}
	}
}

func TestRingStaleFades(t *testing.T) {
	img := Ring(Spec{Percent: p(50), Level: usage.LevelNormal, Stale: true}, 32)
	c := img.NRGBAAt(29, 16)
	eq(t, c, color.NRGBA{Normal.R, Normal.G, Normal.B, 0x80})
}

func TestNumberFits16(t *testing.T) {
	img := Ring(Spec{Percent: p(100), Level: usage.LevelCritical, Number: true}, 16)
	white := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if img.NRGBAAt(x, y) == White {
				white++
				if x == 0 || x == 15 {
					t.Fatalf("digit pixel in column %d", x)
				}
			}
		}
	}
	if white == 0 {
		t.Fatal("no digit pixels")
	}
}

func TestNumberDigits(t *testing.T) {
	// 71% at 16 px: "71" is 7 columns wide (3+1+3), centred, 5 rows high
	img := Ring(Spec{Percent: p(71.4), Level: usage.LevelNormal, Number: true}, 16)
	minX, maxX, minY, maxY := 99, -1, 99, -1
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if img.NRGBAAt(x, y) == White {
				minX, maxX, minY, maxY = min(minX, x), max(maxX, x), min(minY, y), max(maxY, y)
			}
		}
	}
	eq(t, maxX-minX+1, 7)
	eq(t, maxY-minY+1, 5)
	// an outline pixel next to a digit pixel
	eq(t, img.NRGBAAt(minX-1, minY), Outline)
	// digits scale with the size: 48 px draws them 3 times as large
	big := Ring(Spec{Percent: p(71.4), Level: usage.LevelNormal, Number: true}, 48)
	n := 0
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			if big.NRGBAAt(x, y) == White {
				n++
			}
		}
	}
	small := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if img.NRGBAAt(x, y) == White {
				small++
			}
		}
	}
	eq(t, n, small*9)
}

func TestICOHeader(t *testing.T) {
	b := TrayImage("windows", Spec{Percent: p(42), Level: usage.LevelNormal})
	// the notification area's icon size at 100 % to 400 % display scale
	// (SM_CXSMICON at 96, 120, 144, 168, 192, 216, 240, 288, 336, 384 DPI):
	// the tray loads the entry of exactly that size
	sizes := []int{16, 20, 24, 28, 32, 36, 40, 48, 56, 64}
	eq(t, string(b[:6]), "\x00\x00\x01\x00"+string(rune(len(sizes)))+"\x00")
	for i := range sizes {
		e := b[6+16*i:]
		eq(t, int(e[0]), sizes[i])
		eq(t, int(e[1]), sizes[i])
		eq(t, binary.LittleEndian.Uint16(e[6:]), uint16(32))
		size := binary.LittleEndian.Uint32(e[8:])
		off := binary.LittleEndian.Uint32(e[12:])
		data := b[off : off+size]
		eq(t, string(data[:4]), "\x89PNG")
		img, err := png.Decode(bytes.NewReader(data))
		must(t, err)
		eq(t, img.Bounds().Dx(), sizes[i])
	}
}

func TestICO256WidthByteIsZero(t *testing.T) {
	b := ICO(Ring(Spec{Level: usage.LevelNone}, 256))
	eq(t, b[6], byte(0))
	eq(t, b[7], byte(0))
}

func TestTrayImageSizes(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		img, err := png.Decode(bytes.NewReader(TrayImage(goos, Spec{Percent: p(42), Level: usage.LevelNormal})))
		must(t, err)
		eq(t, img.Bounds().Dx(), 32)
	}
	// macOS shows the number as text next to the icon, so the icon has none
	img, _ := png.Decode(bytes.NewReader(TrayImage("darwin", Spec{Percent: p(42), Level: usage.LevelNormal, Number: true})))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if r, g, b, a := img.At(x, y).RGBA(); a == 0xffff && r == 0xffff && g == 0xffff && b == 0xffff {
				t.Fatalf("white digit pixel at %d,%d", x, y)
			}
		}
	}
}

func TestClawdRejectsCurves(t *testing.T) {
	for _, d := range []string{"M0 0C1 1 2 2 3 3Z", "m0 0h1v1h-1z", "M0 0L1 1Z"} {
		svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="` + d + `" fill="#D97757"/></svg>`)
		if _, _, err := svgRects(svg); err == nil {
			t.Fatalf("%q accepted", d)
		}
	}
	two := []byte(`<svg><path d="M0 0V1H1V0Z" fill="#000"/><path d="M0 0V1H1V0Z" fill="#000"/></svg>`)
	if _, _, err := svgRects(two); err == nil {
		t.Fatal("two paths accepted")
	}
}

// Linux draws the number in the icon only when asked to (desktops that
// ignore the label); without a provider that is the white ring digits.
func TestLinuxTrayImageNumberOnRequest(t *testing.T) {
	white := func(number bool) int {
		img, err := png.Decode(bytes.NewReader(TrayImage("linux", Spec{Percent: p(42), Level: usage.LevelNormal, Number: number})))
		must(t, err)
		n := 0
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				if r, g, b, a := img.At(x, y).RGBA(); a == 0xffff && r == 0xffff && g == 0xffff && b == 0xffff {
					n++
				}
			}
		}
		return n
	}
	if white(true) == 0 {
		t.Fatal("no digits drawn when asked for")
	}
	eq(t, white(false), 0)
}
