// winmenu opens the AI Usage tray menu on a Windows CI runner and measures
// it, and changes the display scale for that (agent/ci/check-windows.ps1,
// plan Task 8; CI only, not shipped). It refuses to run outside GitHub
// Actions: it changes the display scale and the screen mode.
//
//	winmenu open -pid PID -png FILE
//	    first opens a menu of its own whose owner-drawn items measure
//	    exactly the panel's width (what Windows adds to a measured width),
//	    then posts the tray icon's right-click to the SystrayClass window
//	    of process PID (plan PR-22), waits for its menu, writes the menu's
//	    picture to FILE and prints one JSON object: the menu window's size
//	    and DPI, the menu font's line, each item's rectangle and type, the
//	    colours of the frame and of the menu's padding on all four sides,
//	    the header's and Quit's background, the system's menu and
//	    highlight colours, the bar's height in the first row, and the
//	    highlight with the pointer on the first row and on Quit; then
//	    closes the menu.
//	winmenu broadcast
//	    sends every window WM_SETTINGCHANGE "ImmersiveColorSet" (what
//	    Windows sends when the colour mode changes).
//	winmenu scale -to 125|150|restore -state FILE
//	    sets the primary display's scale (and, when the screen is too small
//	    for it, a 1920×1080 mode that is not saved), keeping what it was in
//	    FILE; restore puts both back. Prints one JSON object.
package main

import (
	"fmt"
	"math"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// item is one item of the open menu.
type item struct {
	Left, Top, Right, Bottom int32
	Owner, Sep, Disabled     bool
}

func (i item) w() int32 { return i.Right - i.Left }
func (i item) h() int32 { return i.Bottom - i.Top }

// roles finds the panel's first header (the first disabled item that is
// not a separator), its first row (the item after it) and Quit (the last).
func roles(items []item) (header, row, quit int, err error) {
	header = -1
	for i, it := range items {
		if it.Disabled && !it.Sep {
			header = i
			break
		}
	}
	if header < 0 || header+1 >= len(items) || len(items) < 6 {
		return 0, 0, 0, fmt.Errorf("no header and row among %d items", len(items))
	}
	return header, header + 1, len(items) - 1, nil
}

// heightProblems: an item whose height is none the panel draws at the
// metrics' DPI with a menu font of line px a line (a header with or
// without a problem line, a row, a text item, a separator; a problem line
// or a text item wrapped to up to four lines, each further line a line and
// 2 px), the first header not a header's, the first row not a row's, and
// an item that is not owner-drawn.
func heightProblems(items []item, m panel.Metrics, line int) []string {
	var out []string
	px := func(n int) int { return (n*m.Pad + 8) / 16 }
	problem, text := max(m.Problem, line+px(12)), max(m.Item, line+px(12))
	header := map[int32]bool{int32(m.Header): true}
	ok := map[int32]bool{int32(m.Header): true, int32(m.Row): true, int32(m.Separator): true}
	for k := range 4 {
		more := k * (line + px(2))
		header[int32(m.Header+problem+more)] = true
		ok[int32(m.Header+problem+more)], ok[int32(text+more)] = true, true
	}
	for i, it := range items {
		if !it.Owner {
			out = append(out, fmt.Sprintf("item %d not owner-drawn", i))
		}
		if !ok[it.h()] {
			out = append(out, fmt.Sprintf("item %d is %d px high", i, it.h()))
		}
	}
	if h, r, _, err := roles(items); err != nil {
		out = append(out, err.Error())
	} else {
		if !header[items[h].h()] {
			out = append(out, fmt.Sprintf("header %d px, want %d (or with its problem line)", items[h].h(), m.Header))
		}
		if items[r].h() != int32(m.Row) {
			out = append(out, fmt.Sprintf("row %d px, want %d", items[r].h(), m.Row))
		}
	}
	return out
}

// barRun is the bar's height in a column of the first row: the longest
// run of pixels in the bars' warm colours (the normal #d97757, the
// critical #f66151, as the menu draws them over its background), which the
// texts around the bar (black, white, greys) never are.
func barRun(column []rgb) int {
	best, run := 0, 0
	for _, c := range column {
		if int(c.R) > int(c.G)+40 && int(c.R) > int(c.B)+60 {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

// rgb is a sampled colour.
type rgb struct{ R, G, B uint8 }

func (c rgb) String() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// diff is the largest difference of one channel.
func diff(a, b rgb) int {
	d := 0
	for _, v := range []int{int(a.R) - int(b.R), int(a.G) - int(b.G), int(a.B) - int(b.B)} {
		d = max(d, int(math.Abs(float64(v))))
	}
	return d
}

// lum is the colour's luminance, 0 to 1.
func lum(c rgb) float64 {
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
}
