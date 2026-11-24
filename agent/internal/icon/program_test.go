package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

// designs are the program icon in use and the runner-up (see Program).
var designs = map[string]Design{"SplitFace": SplitFace, "TwinBars": TwinBars}

// The program ICO has one entry per size, each the design drawn for that
// size (not one picture scaled).
func TestProgramICOHasEverySize(t *testing.T) {
	b := ProgramICO()
	eq(t, int(binary.LittleEndian.Uint16(b[4:])), len(ProgramIconSizes))
	for i, size := range ProgramIconSizes {
		e := b[6+16*i:]
		eq(t, int(e[0]), size%256) // 256 is stored as 0
		off := binary.LittleEndian.Uint32(e[12:])
		n := binary.LittleEndian.Uint32(e[8:])
		img, err := png.Decode(bytes.NewReader(b[off : off+n]))
		must(t, err)
		eq(t, img.Bounds().Dx(), size)
		eq(t, img.Bounds().Dy(), size)
		if !bytes.Equal(b[off:off+n], PNG(Program(size))) {
			t.Fatalf("the %d px entry is not Program(%d)", size, size)
		}
	}
	for _, want := range []int{16, 20, 24, 32, 40, 48, 64, 256} {
		found := false
		for _, s := range ProgramIconSizes {
			found = found || s == want
		}
		if !found {
			t.Fatalf("no %d px entry", want)
		}
	}
}

func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) int { return max(int(x), int(y)) - min(int(x), int(y)) }
	return a.A == 0xff && d(a.R, b.R) <= 8 && d(a.G, b.G) <= 8 && d(a.B, b.B) <= 8
}

// Half and half: the warm colour only in the left half, the cool one only
// in the right, in about the same amounts (for the gauge, mirror images).
func TestProgramIconIsHalfWarmHalfCool(t *testing.T) {
	for name, d := range designs {
		for _, size := range ProgramIconSizes {
			img := d(size)
			var warm, cool int
			for y := 0; y < size; y++ {
				for x := 0; x < size; x++ {
					c := img.NRGBAAt(x, y)
					left := x < size/2
					switch {
					case near(c, ProgramWarm):
						if !left {
							t.Fatalf("%s %d px: warm at %d,%d (right half)", name, size, x, y)
						}
						warm++
					case near(c, ProgramCool):
						if left {
							t.Fatalf("%s %d px: cool at %d,%d (left half)", name, size, x, y)
						}
						cool++
					}
				}
			}
			if warm == 0 || cool == 0 {
				t.Fatalf("%s %d px: warm %d, cool %d pixels", name, size, warm, cool)
			}
			if name == "SplitFace" && math.Abs(float64(warm-cool)) > 0.25*float64(warm+cool)/2 {
				t.Fatalf("%s %d px: warm %d, cool %d pixels", name, size, warm, cool)
			}
		}
	}
}

// The gauge is round and centred: transparent corners, and its outline is
// the same on both sides.
func TestProgramIconIsARoundGauge(t *testing.T) {
	for _, size := range ProgramIconSizes {
		img := SplitFace(size)
		for _, p := range []image.Point{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
			eq(t, img.NRGBAAt(p.X, p.Y).A, uint8(0))
		}
		for y := 0; y < size; y++ {
			for x := 0; x < size/2; x++ {
				a, b := int(img.NRGBAAt(x, y).A), int(img.NRGBAAt(size-1-x, y).A)
				if a-b > 2 || b-a > 2 {
					t.Fatalf("%d px: alpha %d at %d,%d but %d mirrored", size, a, x, y, b)
				}
			}
		}
		// the scale arc and needle are drawn in the light colour
		if n := count(img, ProgramLight); n == 0 {
			t.Fatalf("%d px: no light scale or needle", size)
		}
	}
}

func count(img *image.NRGBA, c color.NRGBA) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if near(img.NRGBAAt(x, y), c) {
				n++
			}
		}
	}
	return n
}

func luminance(c color.NRGBA) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

func contrast(a, b color.NRGBA) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// Both halves stand out on a light and on a dark background (Windows'
// light and dark themes), and the light gauge stands out on both halves.
func TestProgramColoursWorkOnLightAndDark(t *testing.T) {
	light := color.NRGBA{0xf3, 0xf3, 0xf3, 0xff}
	dark := color.NRGBA{0x20, 0x20, 0x20, 0xff}
	for _, c := range []color.NRGBA{ProgramWarm, ProgramCool} {
		for _, bg := range []color.NRGBA{light, dark} {
			if r := contrast(c, bg); r < 3 {
				t.Fatalf("%v on %v: contrast %.2f", c, bg, r)
			}
		}
		if r := contrast(ProgramLight, c); r < 3 {
			t.Fatalf("gauge on %v: contrast %.2f", c, r)
		}
	}
}

// Our own tones: the warm half is not Anthropic's terracotta (#D97757,
// Clawd's and the tray's colour), nor near it.
func TestProgramColoursAreOurOwn(t *testing.T) {
	clawd := color.NRGBA{0xd9, 0x77, 0x57, 0xff}
	for _, c := range []color.NRGBA{ProgramWarm, ProgramCool, ProgramLight} {
		d := math.Sqrt(math.Pow(float64(c.R)-float64(clawd.R), 2) + math.Pow(float64(c.G)-float64(clawd.G), 2) + math.Pow(float64(c.B)-float64(clawd.B), 2))
		if d < 32 {
			t.Fatalf("%v is %.0f from #D97757", c, d)
		}
	}
}
