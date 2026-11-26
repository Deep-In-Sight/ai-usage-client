// pixcheck measures the usage panel in a screenshot of a GNOME Shell
// (CI only, agent/ci/panel-session.sh; not shipped). Results go to
// standard output, one line each:
//
//	pixcheck bars FILE
//	    bar y=<y> x=<x0> fill=<px> track=<px> colour=#rrggbb alpha=<a> bg=#rrggbb
//	        base=#rrggbb trackcolour=#rrggbb|none height=<px> ends=round|square
//	    for each bar found: a horizontal run of a bar colour (base: the
//	    panel's normal, warning or critical colour, also seen through an
//	    opacity alpha over the background bg: a stale group), followed by a
//	    run of the track colour (none for a full bar: plan ruling F19) and
//	    then the background, at least 100 px in all and in at least four
//	    rows one under the other (a bar is 8 px high; text and icons are
//	    not). y and the lengths are those of its middle row; height is
//	    the fill's rows down the fill's middle column, ends are round when
//	    the top and bottom pixels of the two outermost columns at each end
//	    show the background more than the bar.
//	pixcheck judge -want P:LEVEL[:ALPHA],... FILE
//	    the bars, then "problems: none" or "problems: …": the bars are not
//	    as wanted, top to bottom (design 5.2, plan constraint 7): their
//	    number; fill+track equal for all (±2 px, so a full bar is as long
//	    as the others); the fill of P % of that (panel.FillWidth, ±2 px);
//	    the level's colour seen at ALPHA (1 when left out; a stale group
//	    0.6, ±0.05); the track rgba(128,128,128,0.32) at ALPHA over the
//	    background (±4 per channel); 8 px high; round ends.
//	pixcheck problem -menu x,y,w,h -rect x,y,w,h FILE
//	    bg=#rrggbb light|dark want=#rrggbb pixels=<n>: the menu's
//	    background (its commonest colour) decides light or dark (relative
//	    luminance below 0.5: dark), which decides the problem line's colour
//	    (#9a6700 on light, #f5c211 on dark: design 5.2); n is the pixels of
//	    that colour (±8) in the problem line's rectangle.
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
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: pixcheck bars FILE | judge -want P:LEVEL[:ALPHA],... FILE | colour [-tol N] [-rect x,y,w,h] FILE #rrggbb | ink -rect x,y,w,h FILE | problem -menu x,y,w,h -rect x,y,w,h FILE")
		return 2
	}
	fs := flag.NewFlagSet("pixcheck "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	tol := fs.Int("tol", 0, "tolerance per channel")
	rectArg := fs.String("rect", "", "x,y,w,h")
	menuArg := fs.String("menu", "", "x,y,w,h (problem: the menu)")
	wantArg := fs.String("want", "", "P:LEVEL[:ALPHA],... (judge: the bars top to bottom)")
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
	case "judge":
		wants, err := parseWants(*wantArg)
		if err != nil {
			return fail(err)
		}
		bars := Bars(img)
		for _, b := range bars {
			fmt.Fprintln(out, b)
		}
		probs := Judge(bars, wants)
		if len(probs) == 0 {
			probs = []string{"none"}
		}
		fmt.Fprintln(out, "problems:", strings.Join(probs, " "))
	case "problem":
		if *rectArg == "" || *menuArg == "" {
			return fail(errors.New("problem needs -menu and -rect"))
		}
		menu, err := parseRect(*menuArg)
		if err != nil {
			return fail(err)
		}
		bg, dark, want, n := Problem(img, menu.Intersect(img.Bounds()), r)
		shade := "light"
		if dark {
			shade = "dark"
		}
		fmt.Fprintf(out, "bg=%s %s want=%s pixels=%d\n", hex(bg), shade, hex(want), n)
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
	Base              color.NRGBA // the panel's bar colour Colour is (at Alpha over what is under it)
	TrackColour       color.NRGBA // the track's colour (when Track > 0)
	Height            int         // rows of the fill's colour down the fill's middle column
	BoxHeight         int         // rows that are not the background down the bar's middle column: the track's height where it is higher than the fill
	Round             bool        // the corner pixels of its two outermost columns at each end show the background more than the bar
}

func (b Bar) String() string {
	tc := "none"
	if b.Track > 0 {
		tc = hex(b.TrackColour)
	}
	ends := "square"
	if b.Round {
		ends = "round"
	}
	return fmt.Sprintf("bar y=%d x=%d fill=%d track=%d colour=%s alpha=%.2f bg=%s base=%s trackcolour=%s height=%d ends=%s box=%d",
		b.Y, b.X, b.Fill, b.Track, hex(b.Colour), b.Alpha, hex(b.Bg), hex(b.Base), tc, b.Height, ends, b.BoxHeight)
}

const (
	runTol   = 6   // a bar's fill pixels: within this of each other
	minTotal = 100 // fill + track at least (a bar is 300 px wide)
	minRows  = 4   // rows of the same bar one under the other
)

// barAlpha: c is the panel's bar colour base seen through opacity alpha
// over bg (1 when drawn as it is); ok false when it is none of them.
func barAlpha(c, bg color.NRGBA) (alpha float64, base color.NRGBA, ok bool) {
	for _, b := range []color.NRGBA{panel.Normal, panel.Warning, panel.Critical} {
		if near(c, b, runTol) {
			return 1, b, true
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
			return a, b, true
		}
	}
	return 0, color.NRGBA{}, false
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
		a, base, ok := 0.0, color.NRGBA{}, false
		if hasTrack {
			a, base, ok = barAlpha(mid, track)
		}
		if !ok {
			a, base, ok = barAlpha(mid, bg)
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
			found = append(found, Bar{Y: y, X: x0, Fill: fill, Track: tr, Colour: mid, Bg: bg, Alpha: a, Base: base, TrackColour: track})
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
		bars = append(bars, shape(img, g.rows[len(g.rows)/2]))
	}
	// top to bottom (groups end in any order)
	for i := 1; i < len(bars); i++ {
		for j := i; j > 0 && bars[j].Y < bars[j-1].Y; j-- {
			bars[j], bars[j-1] = bars[j-1], bars[j]
		}
	}
	return bars
}

// shape measures b's height down the middle column of its fill (the rows
// of the fill's colour: a fill lower than its track shows), the height of
// its box down the bar's middle column (the track and the fill on it: a
// track higher than its fill shows, review of stage 5a M1) and whether its
// ends are round: in the bar's box (the rows that are not the background
// down its middle column), the top and bottom pixels of the two outermost
// columns at each end are nearer the background than the pixel of b's
// middle row in that column. A square end's corner is the bar's own
// colour; so is the second column's of an end barely rounded (a corner
// radius of a pixel), while the panel's capsule (radius 4 of 8 px) leaves
// it about a fifth covered.
func shape(img *image.NRGBA, b Bar) Bar {
	r := img.Bounds()
	extent := func(x int, in func(color.NRGBA) bool) (top, bottom int) {
		top, bottom = b.Y, b.Y
		for top-1 >= r.Min.Y && in(at(img, x, top-1)) {
			top--
		}
		for bottom+1 < r.Max.Y && in(at(img, x, bottom+1)) {
			bottom++
		}
		return top, bottom
	}
	top, bottom := extent(b.X+(b.Fill+b.Track)/2, func(c color.NRGBA) bool { return !near(c, b.Bg, 8) })
	ft, fb := extent(b.X+b.Fill/2, func(c color.NRGBA) bool { return near(c, b.Colour, runTol) })
	b.Height = fb - ft + 1
	b.BoxHeight = bottom - top + 1
	end := b.X + b.Fill + b.Track
	b.Round = bottom-top+1 > 2 && b.Fill+b.Track >= 4
	for _, x := range []int{b.X, b.X + 1, end - 2, end - 1} {
		inside := at(img, x, b.Y)
		for _, y := range []int{top, bottom} {
			if c := at(img, x, y); dist(c, b.Bg) >= dist(c, inside) {
				b.Round = false
			}
		}
	}
	return b
}

// Want is what one bar should be: the percent shown, its level's colour,
// seen at alpha (1, or a stale group's 0.6).
type Want struct {
	Percent float64
	Colour  color.NRGBA
	Alpha   float64
}

// parseWants reads "42:normal,100:critical,13:normal:0.6".
func parseWants(s string) ([]Want, error) {
	if s == "" {
		return nil, errors.New("judge needs -want")
	}
	var out []Want
	for _, f := range strings.Split(s, ",") {
		p := strings.Split(f, ":")
		if len(p) < 2 || len(p) > 3 {
			return nil, fmt.Errorf("want %q: P:LEVEL[:ALPHA]", f)
		}
		pc, err := strconv.ParseFloat(p[0], 64)
		if err != nil {
			return nil, fmt.Errorf("want %q: %v", f, err)
		}
		c, ok := panel.BarColour(p[1])
		if !ok {
			return nil, fmt.Errorf("want %q: no level %q", f, p[1])
		}
		w := Want{Percent: pc, Colour: c, Alpha: 1}
		if len(p) == 3 {
			if w.Alpha, err = strconv.ParseFloat(p[2], 64); err != nil || w.Alpha <= 0 || w.Alpha > 1 {
				return nil, fmt.Errorf("want %q: alpha", f)
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// Tolerances of Judge.
const (
	lengthTol = 2    // px: a fill, and fill+track against the others'
	alphaTol  = 0.05 // a stale group's opacity
	trackTol  = 4    // per channel: the track's colour
)

// Judge compares bars, top to bottom, with wants (design 5.2, plan
// constraint 7); nil when they are as wanted.
func Judge(bars []Bar, wants []Want) []string {
	var probs []string
	if len(bars) != len(wants) {
		probs = append(probs, fmt.Sprintf("bars=%d(want_%d)", len(bars), len(wants)))
	}
	n := min(len(bars), len(wants))
	if n == 0 {
		return probs
	}
	// the bars' common length: the median of fill+track
	totals := make([]int, n)
	for i := range n {
		totals[i] = bars[i].Fill + bars[i].Track
	}
	sorted := slices.Clone(totals)
	slices.Sort(sorted)
	ref := sorted[len(sorted)/2]
	for i := range n {
		b, w := bars[i], wants[i]
		if abs(totals[i]-ref) > lengthTol {
			probs = append(probs, fmt.Sprintf("bar%d:length=%d(others_%d)", i, totals[i], ref))
		}
		pc := w.Percent
		if want := panel.FillWidth(&pc, ref, panel.BarHeight); abs(b.Fill-want) > lengthTol {
			probs = append(probs, fmt.Sprintf("bar%d:fill=%d(want_%d_of_%d)", i, b.Fill, want, ref))
		}
		if b.Base != w.Colour {
			probs = append(probs, fmt.Sprintf("bar%d:colour=%s(want_%s)", i, hex(b.Base), hex(w.Colour)))
		}
		if w.Alpha == 1 && b.Alpha != 1 || w.Alpha != 1 && math.Abs(b.Alpha-w.Alpha) > alphaTol {
			probs = append(probs, fmt.Sprintf("bar%d:alpha=%.2f(want_%.2f)", i, b.Alpha, w.Alpha))
		}
		if b.Track > 0 {
			if want := over(panel.Track, b.Bg, w.Alpha); !near(b.TrackColour, want, trackTol) {
				probs = append(probs, fmt.Sprintf("bar%d:track=%s(want_%s)", i, hex(b.TrackColour), hex(want)))
			}
		}
		if b.Height != panel.BarHeight {
			probs = append(probs, fmt.Sprintf("bar%d:height=%d(want_%d)", i, b.Height, panel.BarHeight))
		}
		if b.BoxHeight != panel.BarHeight {
			probs = append(probs, fmt.Sprintf("bar%d:box_height=%d(want_%d)", i, b.BoxHeight, panel.BarHeight))
		}
		if !b.Round {
			probs = append(probs, fmt.Sprintf("bar%d:square_ends", i))
		}
	}
	return probs
}

// over is c, its own alpha times opacity, drawn over bg.
func over(c, bg color.NRGBA, opacity float64) color.NRGBA {
	a := float64(c.A) / 255 * opacity
	mix := func(f, b uint8) uint8 { return uint8(float64(f)*a + float64(b)*(1-a) + 0.5) }
	return color.NRGBA{mix(c.R, bg.R), mix(c.G, bg.G), mix(c.B, bg.B), 0xff}
}

// The problem line's colours (design 5.2): on a dark menu, on a light one.
var (
	problemOnDark  = color.NRGBA{0xf5, 0xc2, 0x11, 0xff}
	problemOnLight = color.NRGBA{0x9a, 0x67, 0x00, 0xff}
)

// Problem: the menu's background (the commonest colour of menu), whether
// it is dark (relative luminance below 0.5), the problem line's colour for
// it, and how many pixels of rect have that colour (±8).
func Problem(img *image.NRGBA, menu, rect image.Rectangle) (bg color.NRGBA, dark bool, want color.NRGBA, n int) {
	_, bg = Ink(img, menu)
	dark = luminance(bg) < 0.5
	want = problemOnLight
	if dark {
		want = problemOnDark
	}
	return bg, dark, want, Count(img, rect, want, 8)
}

// luminance is c's relative luminance (sRGB channels).
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
