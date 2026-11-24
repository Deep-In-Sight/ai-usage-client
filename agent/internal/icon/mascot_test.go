package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// gridOf samples an M/V/H/Z rectangle SVG at the centres of a cols×rows
// grid laid over the drawing's bounding box.
func gridOf(t *testing.T, svg []byte, cols, rows int) ([]string, color.NRGBA) {
	t.Helper()
	d, fill, err := onePath(svg)
	must(t, err)
	ink, err := parseHex(fill)
	must(t, err)
	rects, err := parseRects(d)
	must(t, err)
	bb := rects[0]
	for _, r := range rects[1:] {
		bb = rect{math.Min(bb.x0, r.x0), math.Min(bb.y0, r.y0), math.Max(bb.x1, r.x1), math.Max(bb.y1, r.y1)}
	}
	out := make([]string, rows)
	for row := 0; row < rows; row++ {
		var b strings.Builder
		for col := 0; col < cols; col++ {
			ux := bb.x0 + (float64(col)+0.5)*(bb.x1-bb.x0)/float64(cols)
			uy := bb.y0 + (float64(row)+0.5)*(bb.y1-bb.y0)/float64(rows)
			c := byte('.')
			for _, r := range rects {
				if ux >= r.x0 && ux < r.x1 && uy >= r.y0 && uy < r.y1 {
					c = '#'
					break
				}
			}
			b.WriteByte(c)
		}
		out[row] = b.String()
	}
	return out, ink
}

// The tray's Clawd is the GNOME extension's clawd.svg at its own pixel
// grid (11 columns, 8 rows), not a redrawing.
func TestClawdArtIsTheSVGGrid(t *testing.T) {
	art, ok := arts[Claude]
	if !ok {
		t.Fatal("no artwork for Claude")
	}
	rows, ink := gridOf(t, mustRead(t, "../../../claude-usage@localhost/clawd.svg"), 11, 8)
	eq(t, strings.Join(art.Rows, "\n"), strings.Join(rows, "\n"))
	eq(t, art.Ink, ink)
}

// Every artwork is a rectangular grid of '#' and '.' with ink on each edge.
func TestArtworkIsWellFormed(t *testing.T) {
	for p, a := range arts {
		if p == "" || len(a.Rows) == 0 {
			t.Fatalf("%q: empty", p)
		}
		w := len(a.Rows[0])
		for _, r := range a.Rows {
			if len(r) != w || strings.Trim(r, "#.") != "" {
				t.Fatalf("%q: bad row %q", p, r)
			}
		}
		if !strings.Contains(a.Rows[0], "#") || !strings.Contains(a.Rows[len(a.Rows)-1], "#") {
			t.Fatalf("%q: blank top or bottom row", p)
		}
		left, right := false, false
		for _, r := range a.Rows {
			left, right = left || r[0] == '#', right || r[w-1] == '#'
		}
		if !left || !right {
			t.Fatalf("%q: blank side column", p)
		}
		// the 16 px layouts leave the artwork 14 columns and 8 rows
		if w > 14 || len(a.Rows) > 8 {
			t.Fatalf("%q: %dx%d does not fit the 16 px layouts", p, w, len(a.Rows))
		}
	}
}

// findArt returns where art is drawn at scale m in img: every ink cell is
// art.Ink and every blank cell is transparent. ok is false unless there is
// exactly one such place.
func findArt(img *image.NRGBA, a Art, m int) (image.Point, bool) {
	w, h := len(a.Rows[0])*m, len(a.Rows)*m
	b := img.Bounds()
	var found []image.Point
	for oy := 0; oy+h <= b.Dy(); oy++ {
		for ox := 0; ox+w <= b.Dx(); ox++ {
			match := true
			for y := 0; y < h && match; y++ {
				for x := 0; x < w && match; x++ {
					c := img.NRGBAAt(ox+x, oy+y)
					if a.Rows[y/m][x/m] == '#' {
						match = c == a.Ink
					} else {
						match = c.A == 0
					}
				}
			}
			if match {
				found = append(found, image.Pt(ox, oy))
			}
		}
	}
	if len(found) != 1 {
		return image.Point{}, false
	}
	return found[0], true
}

// findArtAnyScale is findArt at the largest whole scale that matches.
func findArtAnyScale(img *image.NRGBA, a Art) (image.Point, int, bool) {
	for m := img.Bounds().Dx() / len(a.Rows[0]); m >= 1; m-- {
		if at, ok := findArt(img, a, m); ok {
			return at, m, true
		}
	}
	return image.Point{}, 0, false
}

func clawdSpec(pct *float64, l usage.Level) Spec {
	return Spec{Provider: Claude, Percent: pct, Level: l}
}

// Without a detected account the icon is the neutral ring of before.
func TestTrayWithoutProviderIsTheRing(t *testing.T) {
	for _, s := range []Spec{{Level: usage.LevelNone}, {Level: usage.LevelNone, Number: true},
		{Percent: p(42), Level: usage.LevelNormal, Number: true}, {Provider: "unknown", Level: usage.LevelNone}} {
		for _, n := range []int{16, 32, 48} {
			want := Ring(Spec{Percent: s.Percent, Level: s.Level, Stale: s.Stale, Number: s.Number}, n)
			if !bytes.Equal(Tray(s, n).Pix, want.Pix) {
				t.Fatalf("%+v at %d px is not the ring", s, n)
			}
		}
	}
}

// The gauge layout (macOS, GNOME): the mascot crisp at a whole scale, a
// bar under it whose fill is the percentage in the level colour, no digits.
func TestTrayGauge(t *testing.T) {
	for _, n := range []int{16, 32} {
		m := n / 16
		img := Tray(clawdSpec(p(50), usage.LevelWarning), n)
		at, ok := findArt(img, arts[Claude], m)
		if !ok {
			t.Fatalf("%d px: Clawd not drawn exactly once at scale %d", n, m)
		}
		artBottom := at.Y + 8*m
		fill, track, other := 0, 0, 0
		minX, maxX := n, -1
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				c := img.NRGBAAt(x, y)
				if y < artBottom || c.A == 0 {
					continue
				}
				switch c {
				case Warning:
					fill++
				case Track:
					track++
				default:
					other++
				}
				minX, maxX = min(minX, x), max(maxX, x)
			}
		}
		if other != 0 || fill == 0 || track == 0 {
			t.Fatalf("%d px: bar fill %d track %d other %d", n, fill, track, other)
		}
		// the bar is as wide as the mascot and starts under its left edge;
		// 50% fills half of it (to an artwork column, m pixels)
		eq(t, minX, at.X)
		eq(t, maxX, at.X+11*m-1)
		rows := (fill + track) / (11 * m)
		if d := fill/rows - 11*m/2; d < -m || d > m {
			t.Fatalf("%d px: 50%% fills %d of %d columns", n, fill/rows, 11*m)
		}
	}
}

func colourCount(img *image.NRGBA, c color.NRGBA) int {
	n := 0
	for i := 0; i+3 < len(img.Pix); i += 4 {
		if img.Pix[i] == c.R && img.Pix[i+1] == c.G && img.Pix[i+2] == c.B && img.Pix[i+3] == c.A {
			n++
		}
	}
	return n
}

func TestTrayGaugeLevelsAndStates(t *testing.T) {
	artInk := 0
	for _, r := range arts[Claude].Rows {
		artInk += strings.Count(r, "#")
	}
	// 0% and no data: track only; the only terracotta is Clawd
	for _, s := range []Spec{clawdSpec(p(0), usage.LevelNormal), clawdSpec(nil, usage.LevelNone)} {
		img := Tray(s, 16)
		eq(t, colourCount(img, Normal), artInk)
		eq(t, colourCount(img, Warning)+colourCount(img, Critical), 0)
		if colourCount(img, Track) == 0 {
			t.Fatalf("%+v: no track", s)
		}
	}
	full := Tray(clawdSpec(p(100), usage.LevelCritical), 16)
	eq(t, colourCount(full, Track), 0)
	if colourCount(full, Critical) == 0 {
		t.Fatal("100% critical: no red")
	}
	normal := Tray(clawdSpec(p(50), usage.LevelNormal), 16)
	if colourCount(normal, Normal) <= artInk {
		t.Fatal("normal level: no terracotta fill")
	}
	stale := clawdSpec(p(50), usage.LevelNormal)
	stale.Stale = true
	img := Tray(stale, 16)
	eq(t, colourCount(img, color.NRGBA{Normal.R, Normal.G, Normal.B, staleAlpha}), colourCount(normal, Normal)-artInk)
	// Clawd itself is never faded
	if _, ok := findArt(img, arts[Claude], 1); !ok {
		t.Fatal("stale: Clawd changed")
	}
}

// readNumber finds the digits drawn in ink from row below (under the
// mascot; findArt shows nothing else is inside the mascot's box) and reads
// them back with the 3×5 font; it fails the test unless they are whole,
// inside the icon and outlined.
func readNumber(t *testing.T, img *image.NRGBA, ink color.NRGBA, below int) (string, int) {
	t.Helper()
	n := img.Bounds().Dx()
	minX, maxX, minY, maxY := n, -1, n, -1
	for y := below; y < n; y++ {
		for x := 0; x < n; x++ {
			if img.NRGBAAt(x, y) == ink {
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if c := img.NRGBAAt(x+dx, y+dy); c != ink && c != Outline {
							t.Fatalf("%d px: digit pixel %d,%d has neighbour %v", n, x, y, c)
						}
					}
				}
				minX, maxX, minY, maxY = min(minX, x), max(maxX, x), min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		t.Fatalf("%d px: no digits", n)
	}
	h := maxY - minY + 1
	if h%5 != 0 {
		t.Fatalf("%d px: digits %d rows high", n, h)
	}
	d := h / 5
	w := maxX - minX + 1
	if (w/d+1)%4 != 0 {
		t.Fatalf("%d px: digits %d columns wide at scale %d", n, w, d)
	}
	count := (w/d + 1) / 4
	var out strings.Builder
	for i := 0; i < count; i++ {
		var g [5]string
		for row := 0; row < 5; row++ {
			var b strings.Builder
			for col := 0; col < 3; col++ {
				x, y := minX+(i*4+col)*d, minY+row*d
				// a font pixel is d×d of ink
				on := img.NRGBAAt(x, y) == ink
				for sy := 0; sy < d; sy++ {
					for sx := 0; sx < d; sx++ {
						if (img.NRGBAAt(x+sx, y+sy) == ink) != on {
							t.Fatalf("%d px: font pixel at %d,%d is not %dx%d", n, x, y, d, d)
						}
					}
				}
				if on {
					b.WriteByte('#')
				} else {
					b.WriteByte('.')
				}
			}
			g[row] = b.String()
		}
		r := '?'
		for ch, glyph := range glyphs {
			if glyph == g {
				r = ch
			}
		}
		out.WriteRune(r)
	}
	if minX < 1 || maxX > n-2 || minY < 1 || maxY > n-2 {
		t.Fatalf("%d px: no room for the outline", n)
	}
	return out.String(), d
}

// The number layout (Windows, Linux desktops without the label): the
// mascot over the level-coloured number, readable at every tray size.
func TestTrayNumberReadableAtEverySize(t *testing.T) {
	for _, n := range []int{16, 20, 24, 32, 48} {
		for _, c := range []struct {
			pct  float64
			want string
		}{{0, "0"}, {7, "7"}, {42.4, "42"}, {71, "71"}, {99.6, "100"}, {100, "100"}, {130, "130"}} {
			s := clawdSpec(p(c.pct), usage.LevelWarning)
			s.Number = true
			img := Tray(s, n)
			at, m, ok := findArtAnyScale(img, arts[Claude])
			if !ok {
				t.Fatalf("%d px %v%%: Clawd not drawn exactly once", n, c.pct)
			}
			got, d := readNumber(t, img, Warning, at.Y+8*m)
			if got != c.want {
				t.Fatalf("%d px: %v%% reads %q, want %q", n, c.pct, got, c.want)
			}
			if n >= 24 && d < 2 {
				t.Fatalf("%d px: %q at scale %d", n, got, d)
			}
		}
	}
}

func TestTrayNumberLevelsAndStates(t *testing.T) {
	for _, c := range []struct {
		l   usage.Level
		ink color.NRGBA
	}{{usage.LevelNormal, Normal}, {usage.LevelWarning, Warning}, {usage.LevelCritical, Critical}} {
		s := clawdSpec(p(71), c.l)
		s.Number = true
		got, _ := readNumber(t, Tray(s, 16), c.ink, 8)
		eq(t, got, "71")
		// old numbers: the artwork fades (as the ring did) and the digits
		// keep their full colour, so they stay readable on a dark taskbar
		s.Stale = true
		img := Tray(s, 16)
		got, _ = readNumber(t, img, c.ink, 8)
		eq(t, got, "71")
		faded := arts[Claude]
		faded.Ink.A = staleAlpha
		if _, ok := findArt(img, faded, 1); !ok {
			t.Fatalf("%s stale: Clawd not faded", c.l)
		}
	}
	// signed in, no number yet: Clawd and a grey "-"
	s := clawdSpec(nil, usage.LevelNone)
	s.Number = true
	img := Tray(s, 16)
	if _, ok := findArt(img, arts[Claude], 1); !ok {
		t.Fatal("no data: Clawd missing")
	}
	// the "-" glyph is the middle row of a 3×5 cell: 3 grey pixels in a row
	var dash []image.Point
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if img.NRGBAAt(x, y) == None {
				dash = append(dash, image.Pt(x, y))
			}
		}
	}
	eq(t, len(dash), 3)
	if dash[0].Y <= 8 || dash[2].Y != dash[0].Y || dash[2].X-dash[0].X != 2 {
		t.Fatalf("dash pixels %v", dash)
	}
	eq(t, colourCount(img, Normal)+colourCount(img, Warning)+colourCount(img, Critical) > 0, true) // Clawd only
}

func pngSize(t *testing.T, b []byte) int {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	must(t, err)
	return img.Bounds().Dx()
}

func TestTrayImagePerOS(t *testing.T) {
	s := clawdSpec(p(42), usage.LevelNormal)
	// Windows: every ICO entry is the number layout, whatever s.Number says
	b := TrayImage("windows", s)
	for i, n := range []int{16, 20, 24, 32, 48} {
		e := b[6+16*i:]
		size, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		withNumber := s
		withNumber.Number = true
		if !bytes.Equal(b[off:off+size], PNG(Tray(withNumber, n))) {
			t.Fatalf("windows %d px entry", n)
		}
	}
	// macOS: 32 px (16 pt at 2x), never the number
	withNumber := s
	withNumber.Number = true
	eq(t, string(TrayImage("darwin", withNumber)), string(PNG(Tray(s, 32))))
	// Linux: 32 px; the caller decides whether the number is in the icon
	eq(t, string(TrayImage("linux", s)), string(PNG(Tray(s, 32))))
	eq(t, string(TrayImage("linux", withNumber)), string(PNG(Tray(withNumber, 32))))
	for _, goos := range []string{"darwin", "linux"} {
		eq(t, pngSize(t, TrayImage(goos, s)), 32)
	}
}

// GNOME draws the 32 px pixmap at 16 px (2:1) on a normal screen and
// macOS at 16 px on a non-Retina one: the 32 px gauge icon must be the
// 16 px one at exactly twice the size, so halving it stays crisp.
func TestTrayGauge32IsTwice16(t *testing.T) {
	stale := clawdSpec(p(71), usage.LevelNormal)
	stale.Stale = true
	for _, s := range []Spec{clawdSpec(p(42), usage.LevelNormal), clawdSpec(p(7), usage.LevelWarning),
		clawdSpec(p(100), usage.LevelCritical), clawdSpec(nil, usage.LevelNone), stale} {
		small, big := Tray(s, 16), Tray(s, 32)
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				if big.NRGBAAt(x, y) != small.NRGBAAt(x/2, y/2) {
					t.Fatalf("%+v: 32 px pixel %d,%d is not 16 px pixel %d,%d", s, x, y, x/2, y/2)
				}
			}
		}
	}
}
