// pixcheck measures the usage panel in a screenshot of a GNOME Shell
// (CI only, agent/ci/panel-session.sh; not shipped). Results go to
// standard output, one line each:
//
//	pixcheck bars FILE
//	    bar y=<y> x=<x0> fill=<px> track=<px> colour=#rrggbb alpha=<a> bg=#rrggbb
//	    for each bar found: a horizontal run of a bar colour (the panel's
//	    normal, warning or critical colour, also seen through an opacity
//	    alpha over the background bg: a stale group), followed by a run
//	    of the track colour (none for a full bar: plan ruling F19) and
//	    then the background, at least 100 px in all and in at least four
//	    rows one under the other (a bar is 8 px high; text and icons are
//	    not). y and the lengths are those of its middle row.
//	pixcheck colour [-tol N] [-rect x,y,w,h] FILE #rrggbb
//	    the number of pixels within N per channel of the colour
//	pixcheck ink -rect x,y,w,h FILE
//	    ink=#rrggbb bg=#rrggbb: in the rectangle, the commonest colour
//	    (the background) and the pixel farthest from it (a text's colour)
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: pixcheck bars FILE | colour [-tol N] [-rect x,y,w,h] FILE #rrggbb | ink -rect x,y,w,h FILE")
		return 2
	}
	fs := flag.NewFlagSet("pixcheck "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	tol := fs.Int("tol", 0, "tolerance per channel")
	rectArg := fs.String("rect", "", "x,y,w,h")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	fail := func(err error) int { fmt.Fprintln(errOut, "pixcheck:", err); return 1 }
	if len(rest) < 1 {
		return fail(errors.New("no file"))
	}
	img, err := load(rest[0])
	if err != nil {
		return fail(err)
	}
	r := img.Bounds()
	if *rectArg != "" {
		if r, err = parseRect(*rectArg); err != nil {
			return fail(err)
		}
		r = r.Intersect(img.Bounds())
	}
	switch args[0] {
	case "bars":
		for _, b := range Bars(img) {
			fmt.Fprintln(out, b)
		}
	case "colour":
		if len(rest) != 2 {
			return fail(errors.New("colour FILE #rrggbb"))
		}
		c, err := parseHex(rest[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(out, Count(img, r, c, *tol))
	case "ink":
		if *rectArg == "" {
			return fail(errors.New("ink needs -rect"))
		}
		ink, bg := Ink(img, r)
		fmt.Fprintf(out, "ink=%s bg=%s\n", hex(ink), hex(bg))
	default:
		return fail(fmt.Errorf("unknown command %q", args[0]))
	}
	return 0
}

func load(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	img := image.NewNRGBA(src.Bounds())
	draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
	return img, nil
}

func parseRect(s string) (image.Rectangle, error) {
	p := strings.Split(s, ",")
	if len(p) != 4 {
		return image.Rectangle{}, fmt.Errorf("rect %q: want x,y,w,h", s)
	}
	var n [4]int
	for i, v := range p {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("rect %q: %v", s, err)
		}
		n[i] = int(f + 0.5)
	}
	return image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3]), nil
}

func parseHex(s string) (color.NRGBA, error) {
	s = strings.TrimPrefix(s, "#")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		return color.NRGBA{}, fmt.Errorf("colour %q: want #rrggbb", s)
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}, nil
}

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func at(img *image.NRGBA, x, y int) color.NRGBA { return img.NRGBAAt(x, y) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// near: every channel within tol.
func near(a, b color.NRGBA, tol int) bool {
	return abs(int(a.R)-int(b.R)) <= tol && abs(int(a.G)-int(b.G)) <= tol && abs(int(a.B)-int(b.B)) <= tol
}

func dist(a, b color.NRGBA) int {
	return abs(int(a.R)-int(b.R)) + abs(int(a.G)-int(b.G)) + abs(int(a.B)-int(b.B))
}

// Count is the number of pixels of r within tol of c.
func Count(img *image.NRGBA, r image.Rectangle, c color.NRGBA, tol int) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if near(at(img, x, y), c, tol) {
				n++
			}
		}
	}
	return n
}

// Ink is the commonest colour of r (its background) and the pixel
// farthest from it.
func Ink(img *image.NRGBA, r image.Rectangle) (ink, bg color.NRGBA) {
	counts := map[color.NRGBA]int{}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			counts[at(img, x, y)]++
		}
	}
	best := -1
	for c, n := range counts {
		if n > best || (n == best && hex(c) < hex(bg)) {
			bg, best = c, n
		}
	}
	far := -1
	for c := range counts {
		if d := dist(c, bg); d > far || (d == far && hex(c) < hex(ink)) {
			ink, far = c, d
		}
	}
	return ink, bg
}

// Bar is one bar found in a picture.
type Bar struct {
	Y, X, Fill, Track int
	Colour, Bg        color.NRGBA
	Alpha             float64
}

func (b Bar) String() string {
	return fmt.Sprintf("bar y=%d x=%d fill=%d track=%d colour=%s alpha=%.2f bg=%s", b.Y, b.X, b.Fill, b.Track, hex(b.Colour), b.Alpha, hex(b.Bg))
}

const (
	runTol   = 6   // a bar's fill pixels: within this of each other
	minTotal = 100 // fill + track at least (a bar is 300 px wide)
	minRows  = 4   // rows of the same bar one under the other
)

// barAlpha: c is one of the panel's bar colours seen through opacity
// alpha over bg (1 when drawn as it is); ok false when it is none.
func barAlpha(c, bg color.NRGBA) (alpha float64, ok bool) {
	for _, b := range []color.NRGBA{panel.Normal, panel.Warning, panel.Critical} {
		if near(c, b, runTol) {
			return 1, true
		}
		// c = a*b + (1-a)*bg per channel: a from the channel where b and
		// bg differ most, then every channel must agree
		ch := func(x color.NRGBA) [3]float64 { return [3]float64{float64(x.R), float64(x.G), float64(x.B)} }
		cb, cc, cg := ch(b), ch(c), ch(bg)
		k, span := 0, 0.0
		for i := range 3 {
			if d := cb[i] - cg[i]; d*d > span*span {
				k, span = i, d
			}
		}
		if span*span < 40*40 {
			continue
		}
		a := (cc[k] - cg[k]) / span
		if a < 0.3 || a > 1.02 {
			continue
		}
		agree := true
		for i := range 3 {
			if want := a*cb[i] + (1-a)*cg[i]; want-cc[i] > 8 || cc[i]-want > 8 {
				agree = false
			}
		}
		if agree {
			return a, true
		}
	}
	return 0, false
}

// rowBars finds the bars' runs in row y. A fill is a run of pixels
// within runTol of each other in a bar colour, seen as it is or through
// an opacity over what is under it: the track (GNOME applies a stale
// group's opacity to the track and to the fill on it, each on its own) or,
// for a full bar, the background. Its two end pixels, partly covered, count
// when they are nearer the fill than what is beside them.
func rowBars(img *image.NRGBA, y int) []Bar {
	var found []Bar
	r := img.Bounds()
	for x := r.Min.X + 4; x < r.Max.X-2; x++ {
		c := at(img, x, y)
		bg := at(img, x-4, y) // left of the bar's rounded start
		if near(c, bg, runTol) {
			continue
		}
		xe := x
		for xe < r.Max.X && near(at(img, xe, y), c, runTol) {
			xe++
		}
		if xe-x < 3 || xe+1 >= r.Max.X {
			continue
		}
		// what follows: the track (a run of one colour) or the background
		var track color.NRGBA
		hasTrack := !near(at(img, xe+1, y), bg, 8)
		if hasTrack {
			track = at(img, xe+1, y)
		}
		mid := at(img, (x+xe)/2, y)
		a, ok := 0.0, false
		if hasTrack {
			a, ok = barAlpha(mid, track)
		}
		if !ok {
			a, ok = barAlpha(mid, bg)
		}
		if !ok {
			x = xe - 1
			continue
		}
		x0 := x
		if dist(at(img, x0-1, y), c) < dist(at(img, x0-1, y), bg) {
			x0-- // the partly covered first pixel
		}
		next := bg
		if hasTrack {
			next = track
		}
		if dist(at(img, xe, y), c) < dist(at(img, xe, y), next) {
			xe++ // the partly covered last pixel
		}
		// the track runs up to the background (its last pixels are the
		// rounded end)
		xt := xe
		if hasTrack {
			for xt < r.Max.X && near(at(img, xt, y), track, 4) {
				xt++
			}
		}
		end := xt
		for end < r.Max.X && end < xt+2 && !near(at(img, end, y), bg, 8) && dist(at(img, end, y), next) < dist(at(img, end, y), bg) {
			end++
		}
		if end >= r.Max.X || !near(at(img, end, y), bg, 12) && !(end < r.Max.X-1 && near(at(img, end+1, y), bg, 8)) {
			x = xe
			continue
		}
		if fill, tr := xe-x0, end-xe; fill+tr >= minTotal {
			found = append(found, Bar{Y: y, X: x0, Fill: fill, Track: tr, Colour: mid, Bg: bg, Alpha: a})
		}
		x = end
	}
	return found
}

// Bars finds the bars of a picture, top to bottom.
func Bars(img *image.NRGBA) []Bar {
	type group struct {
		rows []Bar
		last int
	}
	var open, done []*group
	r := img.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		var next []*group
		for _, b := range rowBars(img, y) {
			var g *group
			for _, o := range open {
				p := o.rows[len(o.rows)-1]
				if o.last == y-1 && abs(p.X-b.X) <= 3 && abs(p.Fill+p.Track-b.Fill-b.Track) <= 4 {
					g = o
					break
				}
			}
			if g == nil {
				g = &group{}
			}
			g.rows, g.last = append(g.rows, b), y
			next = append(next, g)
		}
		for _, o := range open {
			if o.last != y {
				done = append(done, o)
			}
		}
		open = next
	}
	done = append(done, open...)
	var bars []Bar
	seen := map[*group]bool{}
	for _, g := range done {
		if seen[g] || len(g.rows) < minRows {
			continue
		}
		seen[g] = true
		bars = append(bars, g.rows[len(g.rows)/2])
	}
	// top to bottom (groups end in any order)
	for i := 1; i < len(bars); i++ {
		for j := i; j > 0 && bars[j].Y < bars[j-1].Y; j-- {
			bars[j], bars[j-1] = bars[j-1], bars[j]
		}
	}
	return bars
}
