package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// menu is a synthetic picture of the panel's menu: background bg, the
// bars drawn by panel.BarPixels (the same capsules the drawn panel uses)
// at opacity alpha, and things that are not bars (an orange icon, a
// short run of the critical colour as in a "100%" label).
type drawnBar struct {
	x, y, fill int
	bar        color.NRGBA
	alpha      float64
	// a bar drawn wrong (the zero values: as the panel draws it): its
	// width and height, its track colour, square ends
	w, h   int
	track  color.NRGBA
	square bool
}

const trackW, barH = 300, panel.BarHeight

func menu(bg color.NRGBA, bars []drawnBar) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 420, 40+len(bars)*60))
	for i := range img.Pix {
		img.Pix[i] = []byte{bg.R, bg.G, bg.B, 0xff}[i%4]
	}
	for _, b := range bars {
		w, h, trackC := b.w, b.h, b.track
		if w == 0 {
			w = trackW
		}
		if h == 0 {
			h = barH
		}
		if trackC == (color.NRGBA{}) {
			trackC = panel.Track
		}
		pixels := panel.BarPixels
		if b.square {
			pixels = squareBar
		}
		// GNOME draws the track and the fill on it each at the group's
		// opacity (a stale group): the track over the background, then the
		// fill over that
		under := func(px []byte, x, y int, c color.NRGBA) color.NRGBA {
			i := (y*w + x) * 4
			a := float64(px[i+3]) / 255
			mix := func(src byte, u uint8) uint8 {
				v := float64(src) + float64(u)*(1-a) // premultiplied over u
				return uint8(b.alpha*v + (1-b.alpha)*float64(u) + 0.5)
			}
			return color.NRGBA{mix(px[i+2], c.R), mix(px[i+1], c.G), mix(px[i], c.B), 0xff}
		}
		track := pixels(w, h, 0, b.bar, trackC)
		fill := pixels(w, h, b.fill, b.bar, color.NRGBA{})
		for y := range h {
			for x := range w {
				img.SetNRGBA(b.x+x, b.y+y, under(fill, x, y, under(track, x, y, bg)))
			}
		}
	}
	// not bars: a 16 px orange icon, a critical-coloured label stroke
	for y := 4; y < 20; y++ {
		for x := 4; x < 20; x++ {
			img.SetNRGBA(x, y, panel.Normal)
		}
	}
	for y := 22; y < 25; y++ {
		for x := 340; x < 352; x++ {
			img.SetNRGBA(x, y, panel.Critical)
		}
	}
	return img
}

// squareBar is panel.BarPixels without the round ends: every pixel whole.
func squareBar(w, h, fill int, bar, track color.NRGBA) []byte {
	out := make([]byte, w*h*4)
	for y := range h {
		for x := range w {
			c := track
			if x < fill {
				c = bar
			}
			a := float64(c.A) / 255
			i := (y*w + x) * 4
			out[i], out[i+1], out[i+2], out[i+3] = uint8(float64(c.B)*a+0.5), uint8(float64(c.G)*a+0.5), uint8(float64(c.R)*a+0.5), c.A
		}
	}
	return out
}

func pct(v float64) *float64 { return &v }

func fillOf(p float64) int { return panel.FillWidth(pct(p), trackW, barH) }

// the bars of the combined fixture: Claude 42/13/7, Codex 100 (critical)/8
func combinedBars(alphaClaude float64) []drawnBar {
	var bars []drawnBar
	for i, p := range []float64{42, 13, 7, 100, 8} {
		c, a := panel.Normal, 1.0
		if i < 3 {
			a = alphaClaude
		}
		if p == 100 {
			c = panel.Critical
		}
		bars = append(bars, drawnBar{x: 40, y: 40 + i*60, fill: fillOf(p), bar: c, alpha: a})
	}
	return bars
}

func writePNG(t *testing.T, img image.Image) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "shot.png")
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func pixcheck(t *testing.T, args ...string) []string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
	}
	return strings.Split(strings.TrimSpace(out.String()), "\n")
}

// Five bars, light and dark menus: five lines, top to bottom, with the
// fill lengths of the drawn panel (±2 px), a track that fills the rest of
// 300 px (none for the full bar: ruling F19) and the bar colours; the icon
// and the label are not bars.
func TestBarsFindsTheFiveBars(t *testing.T) {
	for _, bg := range []color.NRGBA{{250, 250, 250, 255}, {48, 48, 48, 255}} {
		lines := pixcheck(t, "bars", writePNG(t, menu(bg, combinedBars(1))))
		if len(lines) != 5 {
			t.Fatalf("bg %v: %d lines:\n%s", bg, len(lines), strings.Join(lines, "\n"))
		}
		for i, p := range []float64{42, 13, 7, 100, 8} {
			var y, x, fill, track int
			var colour, alpha, bgs string
			if _, err := fmt.Sscanf(lines[i], "bar y=%d x=%d fill=%d track=%d colour=%s alpha=%s bg=%s", &y, &x, &fill, &track, &colour, &alpha, &bgs); err != nil {
				t.Fatalf("%q: %v", lines[i], err)
			}
			want := fillOf(p)
			if fill < want-2 || fill > want+2 || fill+track < trackW-2 || fill+track > trackW+2 {
				t.Errorf("bg %v bar %d (%v%%): fill %d track %d, want %d of %d", bg, i, p, fill, track, want, trackW)
			}
			if p == 100 && track > 2 {
				t.Errorf("full bar with a track of %d", track)
			}
			wantC := "#d97757"
			if p == 100 {
				wantC = "#f66151"
			}
			if colour != wantC || alpha != "1.00" || bgs != hex(bg) {
				t.Errorf("bg %v bar %d: %s alpha %s bg %s", bg, i, colour, alpha, bgs)
			}
			if y < 40+i*60 || y >= 40+i*60+barH || x < 39 || x > 41 {
				t.Errorf("bar %d at %d,%d", i, x, y)
			}
		}
	}
}

// A stale group (opacity 0.6): its bars are found, in a colour that is
// not the bar colour but 0.6 of the way from the background to it.
func TestBarsSeesADimmedGroup(t *testing.T) {
	bg := color.NRGBA{250, 250, 250, 255}
	lines := pixcheck(t, "bars", writePNG(t, menu(bg, combinedBars(0.6))))
	if len(lines) != 5 {
		t.Fatalf("%d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for i, l := range lines {
		var alpha float64
		if _, err := fmt.Sscanf(l[strings.Index(l, "alpha="):], "alpha=%f", &alpha); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		switch dim := i < 3; {
		case dim && (strings.Contains(l, "colour=#d97757") || alpha < 0.55 || alpha > 0.65):
			t.Errorf("bar %d not at 0.6: %s", i, l)
		case !dim && alpha != 1:
			t.Errorf("bar %d dimmed: %s", i, l)
		}
	}
}

func TestColourAndInk(t *testing.T) {
	bg := color.NRGBA{250, 250, 250, 255}
	p := writePNG(t, menu(bg, nil))
	eq := func(got []string, want string) {
		t.Helper()
		if len(got) != 1 || got[0] != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	eq(pixcheck(t, "colour", p, "#d97757"), "256")
	eq(pixcheck(t, "colour", "-rect", "0,0,10,10", p, "#d97757"), "36")
	eq(pixcheck(t, "colour", "-tol", "6", p, "#f6614b"), "36")
	eq(pixcheck(t, "colour", p, "#f6614b"), "0")
	eq(pixcheck(t, "ink", "-rect", "0,0,30,30", p), "ink=#d97757 bg=#fafafa")
	var out, errOut bytes.Buffer
	if run([]string{"ink", p}, &out, &errOut) == 0 || run([]string{"colour", p, "orange"}, &out, &errOut) == 0 || run([]string{"bars", "/nonexistent.png"}, &out, &errOut) == 0 {
		t.Fatal("a bad call succeeded")
	}
}

const (
	wantCombined = "42:normal,13:normal,7:normal,100:critical,8:normal"
	wantStates   = "42:normal:0.6,13:normal:0.6,7:normal:0.6,100:critical,8:normal"
)

// judge is pixcheck judge's problems line for a picture.
func judge(t *testing.T, img image.Image, want string) string {
	t.Helper()
	lines := pixcheck(t, "judge", "-want", want, writePNG(t, img))
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "problems: ") {
		t.Fatalf("no problems line: %q", lines)
	}
	return strings.TrimPrefix(last, "problems: ")
}

// Review B-M1: the panel as it is drawn, light and dark, its Claude group
// stale or not, has no problem.
func TestJudgeTheDrawnPanel(t *testing.T) {
	for _, bg := range []color.NRGBA{{250, 250, 250, 255}, {48, 48, 48, 255}} {
		if p := judge(t, menu(bg, combinedBars(1)), wantCombined); p != "none" {
			t.Errorf("bg %v combined: %s", bg, p)
		}
		if p := judge(t, menu(bg, combinedBars(0.6)), wantStates); p != "none" {
			t.Errorf("bg %v states: %s", bg, p)
		}
	}
}

// Review B-M1: each way a bar can be drawn wrong is a problem of that bar:
// (a) the track's colour, (b) its height and square ends, (c) a full bar
// shorter than the others, (d) a stale bar at another opacity or in
// another level's colour; and a bar too few.
func TestJudgeFindsWhatIsWrong(t *testing.T) {
	bg := color.NRGBA{250, 250, 250, 255}
	for _, c := range []struct {
		name, want, problem string
		change              func(b []drawnBar) []drawnBar
	}{
		{"track 0.6", wantCombined, "bar0:track=", func(b []drawnBar) []drawnBar {
			b[0].track = color.NRGBA{0x80, 0x80, 0x80, 153}
			return b
		}},
		{"track blue", wantCombined, "bar1:track=", func(b []drawnBar) []drawnBar {
			b[1].track = color.NRGBA{0x60, 0x60, 0xc0, 82}
			return b
		}},
		{"10 px high", wantCombined, "bar2:height=10", func(b []drawnBar) []drawnBar { b[2].h = 10; return b }},
		{"6 px high", wantCombined, "bar0:height=6", func(b []drawnBar) []drawnBar { b[0].h = 6; return b }},
		{"square ends", wantCombined, "bar4:square_ends", func(b []drawnBar) []drawnBar { b[4].square = true; return b }},
		{"short full bar", wantCombined, "bar3:length=250", func(b []drawnBar) []drawnBar {
			b[3].w, b[3].fill = 250, 250
			return b
		}},
		{"stale at 0.8", wantStates, "bar1:alpha=0.80", func(b []drawnBar) []drawnBar { b[1].alpha = 0.8; return b }},
		{"stale in the warning colour", wantStates, "bar2:colour=#f5c211", func(b []drawnBar) []drawnBar {
			b[2].bar = panel.Warning
			return b
		}},
		{"not stale", wantStates, "bar0:alpha=1.00", func(b []drawnBar) []drawnBar { b[0].alpha = 1; return b }},
		{"a bar too few", wantCombined, "bars=4(want_5)", func(b []drawnBar) []drawnBar { return b[:4] }},
	} {
		alpha := 1.0
		if c.want == wantStates {
			alpha = 0.6
		}
		p := judge(t, menu(bg, c.change(combinedBars(alpha))), c.want)
		if !strings.Contains(p, c.problem) {
			t.Errorf("%s: problems %q, want %q", c.name, p, c.problem)
		}
	}
}

// Review B-I1: the problem line's colour follows the menu's measured
// background, not what the extension decided: on a light menu #9a6700, on
// a dark one #f5c211; a line drawn in the dark menu's colour on a light
// menu has none of the colour wanted.
func TestProblemColourFollowsTheBackground(t *testing.T) {
	line := func(bg, ink color.NRGBA) string {
		img := menu(bg, nil)
		for y := 30; y < 34; y++ {
			for x := 100; x < 160; x++ {
				img.SetNRGBA(x, y, ink)
			}
		}
		return pixcheck(t, "problem", "-menu", "0,0,420,40", "-rect", "90,28,80,8", writePNG(t, img))[0]
	}
	light, dark := color.NRGBA{250, 250, 250, 255}, color.NRGBA{48, 48, 48, 255}
	for _, c := range []struct {
		bg, ink color.NRGBA
		want    string
	}{
		{light, problemOnLight, "bg=#fafafa light want=#9a6700 pixels=240"},
		{light, problemOnDark, "bg=#fafafa light want=#9a6700 pixels=0"},
		{dark, problemOnDark, "bg=#303030 dark want=#f5c211 pixels=240"},
		{dark, problemOnLight, "bg=#303030 dark want=#f5c211 pixels=0"},
	} {
		if got := line(c.bg, c.ink); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
	var out, errOut bytes.Buffer
	if run([]string{"problem", "-rect", "0,0,1,1", writePNG(t, menu(light, nil))}, &out, &errOut) == 0 {
		t.Fatal("problem without -menu succeeded")
	}
}

// Review B-M1 (e): the critical percent text is counted in its own
// rectangle (pixcheck colour -rect), not anywhere in the menu.
func TestCriticalTextInItsRectangle(t *testing.T) {
	p := writePNG(t, menu(color.NRGBA{250, 250, 250, 255}, nil))
	if got := pixcheck(t, "colour", "-tol", "8", "-rect", "336,18,20,10", p, "#f66151"); got[0] != "36" {
		t.Errorf("in the label: %q", got)
	}
	if got := pixcheck(t, "colour", "-tol", "8", "-rect", "300,18,20,10", p, "#f66151"); got[0] != "0" {
		t.Errorf("beside it: %q", got)
	}
}
