// Command macmenu is a CI tool (macOS runners only; it refuses to run
// anywhere else): it opens a tray helper's menu-bar menu as a person
// would (a click on the status item, then the pointer and the keys, all
// posted as events), reads the open menu through the accessibility
// interface, captures it with screencapture, and judges the usage panel's
// view items (plan Task 9): their sizes and width, their VoiceOver labels
// and titles, the bars in the capture, a click on a row, Return on a
// highlighted row (reached with Down, Down twice, Up from one of the
// menu's own items, the pointer), Space, Escape, a second view sent to the
// running helper, and Return in the menu of a helper made the active app.
// Each click or key must send exactly one click (or none). It prints one
// JSON object (the measured facts and the problems found); it gives up
// after its deadline and then prints what it had and the step it was at.
// This file is the part that is plain Go: what is expected and how it is
// judged, tested on every OS.
package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// allowed: only on a GitHub Actions runner (the tool posts mouse and key
// events to the whole session).
func allowed(getenv func(string) string) bool {
	return getenv("GITHUB_ACTIONS") == "true" && getenv("RUNNER_TEMP") != ""
}

// Rect is a rectangle in points, from the top left of the main display.
type Rect struct{ X, Y, W, H float64 }

// Item is one item of the open menu as the accessibility interface shows
// it: the menu item (role, title, state, frame) and its first child (a
// view item's view: its role and VoiceOver label).
type Item struct {
	ID         string `json:"id,omitempty"` // the helper's item it was matched to
	Role       string `json:"role"`
	Title      string `json:"title"`
	Label      string `json:"label,omitempty"` // its own description
	Enabled    bool   `json:"enabled"`
	Selected   bool   `json:"selected"`
	Rect       Rect   `json:"rect"`
	ChildRole  string `json:"child_role,omitempty"`
	ChildLabel string `json:"child_label,omitempty"`
	// the view's own AXEnabled (its menu item's isEnabled)
	ChildEnabled bool `json:"child_enabled,omitempty"`
	// the view's own size (0 when it has none)
	ChildW float64 `json:"child_w,omitempty"`
	ChildH float64 `json:"child_h,omitempty"`
}

// Facts are what one run measured.
type Facts struct {
	AXTrusted     bool   `json:"ax_trusted"`
	PostEvents    bool   `json:"post_events"`
	ScreenCapture bool   `json:"screen_capture"`
	Helper        int    `json:"helper_pid"`
	Status        Rect   `json:"status_item"`
	Opened        string `json:"opened"`  // "click", "press" (AXPress) or "" (it did not open)
	Windows       string `json:"windows"` // the helper's windows while its menu is open, "layer:w×h"
	// WinSeen: a window at the pop-up menu level came with the menu (it
	// then says whether the menu is open)
	WinSeen     bool     `json:"win_seen"`
	Trace       []string `json:"trace,omitempty"` // the steps, with the time and what was seen
	last        string
	Menu        Rect    `json:"menu"`
	Items       []Item  `json:"items"`
	Capture     string  `json:"capture,omitempty"`
	CaptureSize string  `json:"capture_size,omitempty"`
	Scale       float64 `json:"scale"`
	// the longest run of the bar's colour in a row's picture, in pixels,
	// and what it should be
	Bars    map[string][2]int `json:"bars,omitempty"`
	Picture int               `json:"picture"` // pixels of the Claude header picture's colour
	// what the helper sent (its click messages, comma separated: each
	// step must send exactly one or none) after each step; "" none
	HeaderClick  string `json:"header_click"`  // what a click on Claude's header sent
	HeaderStayed bool   `json:"header_stayed"` // the menu stayed open after it
	Click        string `json:"click"`
	ClickClosed  bool   `json:"click_closed"`
	DownSelected string `json:"down_selected"` // the item selected after Down
	Highlight    string `json:"highlight"`     // the highlighted row's colour inside its inset, the header's there
	HeaderBg     string `json:"header_bg"`
	// the menu's own highlight (Open dashboard under the pointer) and the
	// view's (5-hour after Down), measured the same way
	NativeHL     *Shape `json:"native_highlight,omitempty"`
	ViewHL       *Shape `json:"view_highlight,omitempty"`
	Return       string `json:"return"`
	ReturnClosed bool   `json:"return_closed"`
	// Return on the row reached with Down twice (Weekly), with Up from
	// Open dashboard (under the pointer) and on the row under the pointer
	// (Weekly) (review A3-I1); what Up highlighted
	Down2         string `json:"down2_return"`
	Down2Closed   bool   `json:"down2_closed"`
	UpSelected    string `json:"up_selected"`
	Up            string `json:"up_return"`
	UpClosed      bool   `json:"up_closed"`
	Pointer       string `json:"pointer_return"`
	PointerClosed bool   `json:"pointer_closed"`
	// Space on Open dashboard under the pointer (the menu's own item) and
	// on 5-hour chosen with Down (review A3-M4)
	SpaceNative       string `json:"space_native"`
	SpaceNativeClosed bool   `json:"space_native_closed"`
	SpaceView         string `json:"space_view"`
	SpaceViewClosed   bool   `json:"space_view_closed"`
	Escape            string `json:"escape"`
	EscapeClosed      bool   `json:"escape_closed"`
	// the menu opened again after the running helper got another view
	// (States, then Combined again: review B3-I4)
	Reopens []Reopen `json:"reopens,omitempty"`
	// Return in the menu of a helper that was made the active app (a CI
	// build's test switch): "sent=IDS closed=BOOL", or "not active …" when
	// it did not become the active app; ActiveSwitch: that helper said it
	// took the switch
	Active       string `json:"active_return"`
	ActiveSwitch bool   `json:"active_switch"`
	// the deadline passed: after which step (the facts are those up to it)
	TimedOut string   `json:"timed_out,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	Problems []string `json:"problems"`
}

// Reopen is the menu read (and captured) again after the running helper
// got the view of another fixture.
type Reopen struct {
	Fixture string  `json:"fixture"` // "states" or "combined"
	Opened  bool    `json:"opened"`
	Menu    Rect    `json:"menu"`
	Items   []Item  `json:"items"`
	Scale   float64 `json:"scale"`
	// States: pixels of the problem colour under Codex's header line, and
	// Claude's 5-hour bar in its faded (not current) colour: its longest
	// run and what it should be
	Problem int      `json:"problem_px"`
	Stale   [2]int   `json:"stale_bar"`
	Errors  []string `json:"errors,omitempty"`
}

// Expected is one item the helper shows, from the fixture: a view item
// (its VoiceOver label) or one of the menu's own items (its title).
type Expected struct {
	ID    string
	View  bool
	Kind  string // a view item's panel kind
	Label string
	Title string
	Item  panel.Item
}

// expected are the visible items of the fixture's menu in order, as the
// macOS helper shows them (the headers, rows and the server line are
// views; the actions are the menu's own).
func expected(in tray.MenuInputs) []Expected {
	p := tray.BuildPanel(in)
	drawn := panel.Items(p)
	var out []Expected
	for _, it := range tray.BuildMenu(in) {
		if !it.Visible {
			continue
		}
		if d, ok := drawn[it.ID]; ok {
			out = append(out, Expected{ID: it.ID, View: true, Kind: d.Kind, Label: panel.AccessibleText(d), Item: d})
			continue
		}
		if strings.HasPrefix(it.ID, "p.") {
			continue // a problem line: drawn by its header
		}
		if it.ID == "server" {
			s := panel.Item{Kind: "info", Text: p.Server, Dim: true}
			out = append(out, Expected{ID: it.ID, View: true, Kind: "info", Label: panel.AccessibleText(s), Item: s})
			continue
		}
		out = append(out, Expected{ID: it.ID, Title: it.Text})
	}
	return out
}

// viewLabel is what VoiceOver gets for a view item: its view's own label
// ("" when it cannot be read: never the menu item's, review B3-I5).
func viewLabel(it Item) string { return it.ChildLabel }

// viewSize is a view item's size: its view's own (0 when it cannot be
// read: never the menu item's, which the accessibility interface gives
// the menu's width whatever the view's, review B3-I5).
func viewSize(it Item) (w, h float64) { return it.ChildW, it.ChildH }

// viewRole is the role of the view that says that label.
func viewRole(it Item) string { return it.ChildRole }

// bars are the text menu's bar characters, which a view item's own title
// must not carry (review A3-M2, B3-m15).
const bars = "█░"

// match names each menu item by the expected item it shows: a view item
// by its view's label, the others by their title.
func match(items []Item, want []Expected) {
	for i := range items {
		for _, w := range want {
			if (w.View && viewLabel(items[i]) == w.Label && w.Label != "") || (!w.View && items[i].Title == w.Title && w.Title != "") {
				items[i].ID = w.ID
				break
			}
		}
	}
}

func byID(items []Item, id string) (Item, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// judgeItems lists what is wrong with the items of an open menu showing
// want: each expected item there (a view item found by its view's label,
// the others by their title), a view item's view in the menu item role,
// as wide as the menu (its widest item) and at least the panel's width,
// as high as its kind is (a header with a problem line taller by at least
// that line; the server line, which wraps to two lines in the fixture, at
// least a line more than a text item), its own title free of the text
// menu's bar characters, a header not clickable; the menu's own items
// without a view.
func judgeItems(items []Item, want []Expected) []string {
	var p []string
	m := panel.MetricsAt(96)
	widest := 0.0
	for _, it := range items {
		widest = math.Max(widest, it.Rect.W)
	}
	for _, w := range want {
		it, ok := byID(items, w.ID)
		if !ok {
			if w.View {
				p = append(p, fmt.Sprintf("%s: no menu item whose view says %q", w.ID, w.Label))
			} else {
				p = append(p, fmt.Sprintf("%s: no menu item %q", w.ID, w.Title))
			}
			continue
		}
		if !w.View {
			if it.ChildLabel != "" {
				p = append(p, fmt.Sprintf("%s: the menu's own item has a view (%q)", w.ID, it.ChildLabel))
			}
			continue
		}
		if viewRole(it) != "AXMenuItem" {
			p = append(p, fmt.Sprintf("%s: its view's role is %q, want AXMenuItem", w.ID, viewRole(it)))
		}
		if strings.ContainsAny(it.Title, bars) {
			p = append(p, fmt.Sprintf("%s: its menu item's title %q has the text menu's bar", w.ID, it.Title))
		}
		vw, vh := viewSize(it)
		if vw <= 0 || vh <= 0 {
			p = append(p, fmt.Sprintf("%s: its view's own size could not be read (%.0fx%.0f)", w.ID, vw, vh))
			continue
		}
		if vw < float64(m.Width) || math.Abs(vw-widest) > 0.5 {
			p = append(p, fmt.Sprintf("%s: %.0f pt wide, want the menu's %.0f (at least %d)", w.ID, vw, widest, m.Width))
		}
		switch {
		case w.Kind == "header" && w.Item.Problem != "":
			if vh < float64(m.Header+m.Problem)-0.5 {
				p = append(p, fmt.Sprintf("%s: %.0f pt high, want at least %d (the header and its problem line)", w.ID, vh, m.Header+m.Problem))
			}
		case w.Kind == "header":
			if math.Abs(vh-float64(m.Header)) > 0.5 {
				p = append(p, fmt.Sprintf("%s: %.0f pt high, want %d", w.ID, vh, m.Header))
			}
		case w.Kind == "row":
			if math.Abs(vh-float64(m.Row)) > 0.5 {
				p = append(p, fmt.Sprintf("%s: %.0f pt high, want %d", w.ID, vh, m.Row))
			}
		case w.ID == "server":
			if vh < float64(m.Item+serverLine)-0.5 {
				p = append(p, fmt.Sprintf("%s: %.0f pt high, want at least %d (two lines)", w.ID, vh, m.Item+serverLine))
			}
		default:
			if vh < float64(m.Item)-0.5 {
				p = append(p, fmt.Sprintf("%s: %.0f pt high, want at least %d", w.ID, vh, m.Item))
			}
		}
		if w.Kind == "header" && it.ChildEnabled {
			p = append(p, fmt.Sprintf("%s: a header can be clicked", w.ID))
		}
	}
	return p
}

// serverLine is the least a second line adds to the server line: a line of
// the menu font (16 pt on macOS 26, measured) and its gap, rounded down
// (review B3-m14: a view cut to one line is 30 pt).
const serverLine = 12

// judge lists what is wrong with a run of the fixture's menu.
func judge(f Facts, want []Expected) []string {
	var p []string
	if !f.AXTrusted {
		p = append(p, "not trusted for accessibility on this runner")
	}
	if !f.PostEvents {
		p = append(p, "may not post events on this runner")
	}
	p = append(p, f.Errors...)
	if f.Opened == "" {
		return append(p, "the menu did not open")
	}
	p = append(p, judgeItems(f.Items, want)...)
	if f.ScreenCapture || f.Capture != "" {
		for id, b := range f.Bars {
			if tol := int(math.Ceil(3 * f.Scale)); b[0] < b[1]-tol || b[0] > b[1]+tol {
				p = append(p, fmt.Sprintf("%s: the bar's colour runs %d px in the capture, want %d", id, b[0], b[1]))
			}
		}
		if f.Picture < 12 {
			p = append(p, fmt.Sprintf("the Claude header's picture: %d pixels of its colour in the capture", f.Picture))
		}
		if f.Highlight == "" || f.Highlight == f.HeaderBg || colourDist(f.Highlight, f.HeaderBg) < 40 {
			p = append(p, fmt.Sprintf("the row chosen with Down is not highlighted: %s, the header %s", f.Highlight, f.HeaderBg))
		}
	}
	if f.HeaderClick != "" {
		p = append(p, fmt.Sprintf("a click on Claude's header sent %q, want nothing", f.HeaderClick))
	}
	if !f.HeaderStayed {
		p = append(p, "the menu closed on a click on Claude's header")
	}
	p = append(p, compareHighlights(f.NativeHL, f.ViewHL)...)
	if f.Click != "r.claude.0" {
		p = append(p, fmt.Sprintf("a click on 5-hour sent %q, want r.claude.0", f.Click))
	}
	if !f.ClickClosed {
		p = append(p, "the menu stayed open after the click")
	}
	if f.DownSelected != "r.claude.0" {
		p = append(p, fmt.Sprintf("Down chose %q, want r.claude.0", f.DownSelected))
	}
	if f.Return != "r.claude.0" {
		p = append(p, fmt.Sprintf("Return on the highlighted 5-hour sent %q, want r.claude.0", f.Return))
	}
	if !f.ReturnClosed {
		p = append(p, "the menu stayed open after Return")
	}
	up := rowAbove(want, "dashboard")
	for _, c := range []struct {
		what, got, want string
		closed          bool
	}{{"Down, Down, Return", f.Down2, "r.claude.1", f.Down2Closed}, {"Up from Open dashboard, Return", f.Up, up, f.UpClosed},
		{"Return on Weekly under the pointer", f.Pointer, "r.claude.1", f.PointerClosed}} {
		if c.got != c.want {
			p = append(p, fmt.Sprintf("%s sent %q, want %s", c.what, c.got, c.want))
		}
		if !c.closed {
			p = append(p, fmt.Sprintf("the menu stayed open after %s", c.what))
		}
	}
	// Space does on a view row what it does on the menu's own items
	switch f.SpaceNative {
	case "dashboard":
		if f.SpaceView != "r.claude.0" || !f.SpaceViewClosed {
			p = append(p, fmt.Sprintf("Space chooses the menu's own items, but on the highlighted 5-hour it sent %q (closed %v)", f.SpaceView, f.SpaceViewClosed))
		}
	case "":
		if f.SpaceView != "" {
			p = append(p, fmt.Sprintf("Space does nothing on the menu's own items, but on the highlighted 5-hour it sent %q", f.SpaceView))
		}
	default:
		p = append(p, fmt.Sprintf("Space on Open dashboard sent %q", f.SpaceNative))
	}
	switch {
	case f.Active == "":
		p = append(p, "Return in the active helper's menu was not measured")
	case strings.HasPrefix(f.Active, "not active"):
		// activation tried and failed: the CI step says so
	case f.Active != "sent=r.claude.0 closed=true":
		p = append(p, fmt.Sprintf("Return on the highlighted 5-hour in the active helper's menu: %s, want sent=r.claude.0 closed=true", f.Active))
	}
	if f.Active != "" && !f.ActiveSwitch {
		p = append(p, "the helper made active did not say it took the test switch")
	}
	if f.Escape != "" {
		p = append(p, fmt.Sprintf("Escape sent %q, want nothing", f.Escape))
	}
	if !f.EscapeClosed {
		p = append(p, "the menu stayed open after Escape")
	}
	p = append(p, judgeReopens(f.Reopens)...)
	return p
}

// rowAbove is the last row before id in want (what Up from id reaches:
// the server line between them cannot be chosen).
func rowAbove(want []Expected, id string) string {
	last := ""
	for _, w := range want {
		if w.ID == id {
			return last
		}
		if w.Kind == "row" {
			last = w.ID
		}
	}
	return ""
}

// judgeReopens: the menu opened again after the States view shows it
// (sized again: Codex's header taller by its problem line, which is drawn
// in its colour; Claude's numbers not current: its bar faded), and again
// after the Combined view (Codex's header 32 pt again).
func judgeReopens(rs []Reopen) []string {
	var p []string
	seen := map[string]bool{}
	for _, r := range rs {
		seen[r.Fixture] = true
		where := "after the " + r.Fixture + " view: "
		for _, e := range r.Errors {
			p = append(p, where+e)
		}
		if !r.Opened {
			p = append(p, where+"the menu did not open")
			continue
		}
		in := panelfixture.Combined()
		if r.Fixture == "states" {
			in = panelfixture.States()
		}
		for _, q := range judgeItems(r.Items, expected(in)) {
			p = append(p, where+q)
		}
		if r.Fixture != "states" {
			continue
		}
		if r.Problem < 8 {
			p = append(p, fmt.Sprintf("%s%d pixels of the problem colour under Codex's header line", where, r.Problem))
		}
		if tol := int(math.Ceil(3 * math.Max(r.Scale, 1))); r.Stale[1] <= 0 || r.Stale[0] < r.Stale[1]-tol || r.Stale[0] > r.Stale[1]+tol {
			p = append(p, fmt.Sprintf("%sClaude's 5-hour bar runs %d px in its faded colour, want %d", where, r.Stale[0], r.Stale[1]))
		}
	}
	for _, fx := range []string{"states", "combined"} {
		if !seen[fx] {
			p = append(p, "the menu was not opened again after the "+fx+" view")
		}
	}
	return p
}

// judgeAgent lists what is wrong with the agent's own helper's menu (real
// data: the ids are not known): it opened, has headers (32 pt) and rows
// (64 pt) with VoiceOver labels, every view as wide as the menu.
func judgeAgent(f Facts) []string {
	var p []string
	p = append(p, f.Errors...)
	if f.Opened == "" {
		return append(p, "the menu did not open")
	}
	m := panel.MetricsAt(96)
	widest := 0.0
	for _, it := range f.Items {
		widest = math.Max(widest, it.Rect.W)
	}
	headers, rows := 0, 0
	for _, it := range f.Items {
		if viewLabel(it) == "" {
			continue
		}
		vw, vh := viewSize(it)
		if vw <= 0 || vh <= 0 {
			p = append(p, fmt.Sprintf("%q: its view's own size could not be read", viewLabel(it)))
			continue
		}
		if strings.ContainsAny(it.Title, bars) {
			p = append(p, fmt.Sprintf("%q: its menu item's title has the text menu's bar", viewLabel(it)))
		}
		switch {
		case math.Abs(vh-float64(m.Header)) <= 0.5:
			headers++
		case math.Abs(vh-float64(m.Row)) <= 0.5:
			rows++
		}
		if vw < float64(m.Width) || math.Abs(vw-widest) > 0.5 {
			p = append(p, fmt.Sprintf("%q: %.0f pt wide, want the menu's %.0f", viewLabel(it), vw, widest))
		}
	}
	if headers < 1 || rows < 1 {
		p = append(p, fmt.Sprintf("%d headers and %d rows drawn as views", headers, rows))
	}
	return p
}

// hexColour is c as #rrggbb.
func hexColour(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func colourDist(a, b string) int {
	var ar, ag, ab, br, bg, bb int
	if _, err := fmt.Sscanf(a, "#%02x%02x%02x", &ar, &ag, &ab); err != nil {
		return 0
	}
	if _, err := fmt.Sscanf(b, "#%02x%02x%02x", &br, &bg, &bb); err != nil {
		return 0
	}
	return iabs(ar-br) + iabs(ag-bg) + iabs(ab-bb)
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// near: within tol on each channel.
func near(c color.Color, want color.NRGBA, tol int) bool {
	r, g, b, _ := c.RGBA()
	return iabs(int(r>>8)-int(want.R)) <= tol && iabs(int(g>>8)-int(want.G)) <= tol && iabs(int(b>>8)-int(want.B)) <= tol
}

// longestRun is the longest horizontal run of pixels near want in r (in
// the picture's pixels).
func longestRun(img image.Image, r image.Rectangle, want color.NRGBA, tol int) int {
	r = r.Intersect(img.Bounds())
	best := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		n := 0
		for x := r.Min.X; x < r.Max.X; x++ {
			if near(img.At(x, y), want, tol) {
				n++
				best = max(best, n)
			} else {
				n = 0
			}
		}
	}
	return best
}

// count is the number of pixels near want in r.
func count(img image.Image, r image.Rectangle, want color.NRGBA, tol int) int {
	r = r.Intersect(img.Bounds())
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if near(img.At(x, y), want, tol) {
				n++
			}
		}
	}
	return n
}

// inPicture is an item's rectangle in the capture of the menu at menu
// (scale pixels per point).
func inPicture(it, menu Rect, scale float64) image.Rectangle {
	px := func(v float64) int { return int(math.Round(v * scale)) }
	return image.Rect(px(it.X-menu.X), px(it.Y-menu.Y), px(it.X-menu.X+it.W), px(it.Y-menu.Y+it.H))
}

// analyse reads the capture of the open menu: the bars of Claude's 5-hour
// (its fill, 42 %) and Codex's 5-hour (100 %, critical) against
// panel.FillWidth across the item's width, and the Claude header's
// picture. Colour tolerance 24: the capture is in the display's colours.
func analyse(f *Facts, img image.Image, want []Expected) {
	m := panel.MetricsAt(96)
	f.Bars = map[string][2]int{}
	for _, id := range []string{"r.claude.0", "r.codex.0"} {
		it, ok := byID(f.Items, id)
		if !ok {
			continue
		}
		var w Expected
		for _, e := range want {
			if e.ID == id {
				w = e
			}
		}
		colour, ok := panel.BarColour(w.Item.Level)
		if !ok {
			continue
		}
		track := int(math.Round((it.Rect.W - float64(2*m.Pad)) * f.Scale))
		bar := int(math.Round(float64(m.Bar) * f.Scale))
		f.Bars[id] = [2]int{longestRun(img, inPicture(it.Rect, f.Menu, f.Scale), colour, 24), panel.FillWidth(w.Item.Percent, track, bar)}
	}
	if it, ok := byID(f.Items, "h.claude"); ok {
		r := inPicture(it.Rect, f.Menu, f.Scale)
		r.Max.X = r.Min.X + int(math.Round(float64(m.Pad+m.Picture)*f.Scale))
		f.Picture = count(img, r, panel.Normal, 24)
	}
}

// analyseStates reads the capture of the menu showing the States view:
// the pixels of the problem colour (light menu) under Codex's header line,
// and the longest run of Claude's 5-hour bar in its faded colour (the
// menu's own background, a pixel of Quit, under it at the stale opacity).
func analyseStates(r *Reopen, img image.Image) {
	m := panel.MetricsAt(96)
	if it, ok := byID(r.Items, "h.codex"); ok {
		rc := inPicture(it.Rect, r.Menu, r.Scale)
		rc.Min.Y += int(math.Round(float64(m.Header) * r.Scale))
		r.Problem = count(img, rc, panel.ProblemOnLight, 40)
	}
	it, ok := byID(r.Items, "r.claude.0")
	bg := bgOf(img, r.Items, "quit", r.Menu, r.Scale)
	if !ok || bg == nil {
		return
	}
	var w Expected
	for _, e := range expected(panelfixture.States()) {
		if e.ID == "r.claude.0" {
			w = e
		}
	}
	cr, cg, cb, _ := bg.RGBA()
	faded := panel.Fade(panel.Normal, color.NRGBA{uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8), 0xff}, panel.StaleOpacity)
	track := int(math.Round((it.Rect.W - float64(2*m.Pad)) * r.Scale))
	bar := int(math.Round(float64(m.Bar) * r.Scale))
	r.Stale = [2]int{longestRun(img, inPicture(it.Rect, r.Menu, r.Scale), faded, 24), panel.FillWidth(w.Item.Percent, track, bar)}
}

// helloAndView are the lines a helper of the fixture gets: its hello and
// the fixture's view (the menu, the panel, the icon and its title).
func helloAndView(token string, in tray.MenuInputs) []trayproto.Msg {
	return []trayproto.Msg{{V: trayproto.Version, Type: trayproto.TypeHello, Token: token, Provider: "claude"}, viewOf(in)}
}

// viewOf is the fixture's view (the menu, the panel, the icon and its
// title).
func viewOf(in tray.MenuInputs) trayproto.Msg {
	p := tray.BuildPanel(in)
	pct := 42.0
	return trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeView, Items: tray.BuildMenu(in), Panel: &p,
		Icon: &trayproto.Icon{Provider: "claude", Percent: &pct, Level: "normal"}, Title: "42%", Tooltip: "AI Usage"}
}

// summary is the run's numbers in one line (a CI notice); for the agent's
// own helper (agent) only what inspect measures.
func summary(f Facts, agent bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "opened by %s; menu %.0fx%.0f pt, capture %s (%.1f px/pt, screen capture %v)", orNone(f.Opened), f.Menu.W, f.Menu.H, orNone(f.CaptureSize), f.Scale, f.ScreenCapture)
	b.WriteString("; items")
	for _, it := range f.Items {
		name := it.ID
		if name == "" {
			name = it.Title
		}
		if name == "" {
			name = "-"
		}
		v := ""
		if viewLabel(it) != "" {
			v = "*"
		}
		w, h := viewSize(it)
		fmt.Fprintf(&b, " %s%s %.0fx%.0f", name, v, w, h)
	}
	for _, id := range []string{"r.claude.0", "r.codex.0"} {
		if bar, ok := f.Bars[id]; ok {
			fmt.Fprintf(&b, "; %s bar %d/%d px", id, bar[0], bar[1])
		}
	}
	if agent {
		fmt.Fprintf(&b, "; Escape closed the menu %v; windows %s", f.EscapeClosed, orNone(f.Windows))
		return b.String()
	}
	fmt.Fprintf(&b, "; picture %d px; highlight %s on %s; highlight native %s, view %s", f.Picture, orNone(f.Highlight), orNone(f.HeaderBg), f.NativeHL, f.ViewHL)
	fmt.Fprintf(&b, "; windows %s (menu window seen %v)", orNone(f.Windows), f.WinSeen)
	if it, ok := byID(f.Items, "r.claude.0"); ok {
		fmt.Fprintf(&b, "; VoiceOver 5-hour: %s %q, its menu item's title %q", viewRole(it), viewLabel(it), it.Title)
	}
	for _, r := range f.Reopens {
		hc := "none"
		if it, ok := byID(r.Items, "h.codex"); ok {
			hc = fmt.Sprintf("%.0fx%.0f", it.ChildW, it.ChildH)
		}
		fmt.Fprintf(&b, "; after the %s view: menu %.0fx%.0f, h.codex %s", r.Fixture, r.Menu.W, r.Menu.H, hc)
		if r.Fixture == "states" {
			fmt.Fprintf(&b, ", problem colour %d px, Claude's faded bar %d/%d px", r.Problem, r.Stale[0], r.Stale[1])
		}
	}
	return b.String()
}

// keys is what the clicks and keys sent, in one line (a CI notice of its
// own: the summary is long).
func keys(f Facts) string {
	return fmt.Sprintf("header click %s (stayed open %v); click %s (closed %v); Down chose %s, Return %s (closed %v); "+
		"Down Down Return %s (closed %v); Up from Open dashboard chose %s, Return %s (closed %v); pointer on Weekly, Return %s (closed %v); "+
		"Space on Open dashboard %s (closed %v), on 5-hour %s (closed %v); Escape %s (closed %v); Return in the active helper's menu: %s (switch taken %v)",
		orNone(f.HeaderClick), f.HeaderStayed, orNone(f.Click), f.ClickClosed, orNone(f.DownSelected), orNone(f.Return), f.ReturnClosed,
		orNone(f.Down2), f.Down2Closed, orNone(f.UpSelected), orNone(f.Up), f.UpClosed, orNone(f.Pointer), f.PointerClosed,
		orNone(f.SpaceNative), f.SpaceNativeClosed, orNone(f.SpaceView), f.SpaceViewClosed, orNone(f.Escape), f.EscapeClosed, orNone(f.Active), f.ActiveSwitch)
}

// Shape is a highlight inside its item, in points: its insets from the
// item's left, right, top and bottom edges, its corners' radius; its
// colour.
type Shape struct {
	Left, Right, Top, Bottom, Corner float64
	Colour                           string
}

func (s *Shape) String() string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("%s inset l%.0f r%.0f t%.0f b%.0f corner %.0f", s.Colour, s.Left, s.Right, s.Top, s.Bottom, s.Corner)
}

// bgOf is the menu's background in a capture: a pixel of the item id
// (one nobody highlights) three quarters across, at its middle.
func bgOf(img image.Image, items []Item, id string, menu Rect, scale float64) color.Color {
	it, ok := byID(items, id)
	if !ok {
		return nil
	}
	r := inPicture(it.Rect, menu, scale)
	return img.At(r.Min.X+r.Dx()*3/4, (r.Min.Y+r.Max.Y)/2)
}

// highlightShape measures the highlight in item rectangle r of a capture
// (scale pixels per point) over background bg: what differs from bg
// (more than 60 over the three channels) in the item's middle row from
// either side, in its middle column from the top and the bottom, and its
// corner's radius from how far in its top row starts (to half a point).
func highlightShape(img image.Image, r image.Rectangle, bg color.Color, scale float64) (Shape, bool) {
	r = r.Intersect(img.Bounds())
	if bg == nil || scale <= 0 || r.Dx() < 20 || r.Dy() < 8 {
		return Shape{}, false
	}
	bgHex := hexColour(bg)
	far := func(x, y int) bool { return colourDist(hexColour(img.At(x, y)), bgHex) > 60 }
	first := func(y int) int {
		for x := r.Min.X; x < r.Max.X; x++ {
			if far(x, y) {
				return x
			}
		}
		return -1
	}
	mid, cx := (r.Min.Y+r.Max.Y)/2, (r.Min.X+r.Max.X)/2
	left, right, top, bottom := first(mid), -1, -1, -1
	for x := r.Max.X - 1; x >= r.Min.X; x-- {
		if far(x, mid) {
			right = x
			break
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if far(cx, y) {
			top = y
			break
		}
	}
	for y := r.Max.Y - 1; y >= r.Min.Y; y-- {
		if far(cx, y) {
			bottom = y
			break
		}
	}
	if left < 0 || right <= left || top < 0 || bottom <= top {
		return Shape{}, false
	}
	// a circle of radius r cuts i = r - sqrt(r² - (r-½)²) pixels off its
	// top row: r = i + ½ + √i
	corner := 0.0
	if x := first(top); x > left {
		i := float64(x - left)
		corner = i + 0.5 + math.Sqrt(i)
	}
	pt := func(v int) float64 { return float64(v) / scale }
	return Shape{Left: pt(left - r.Min.X), Right: pt(r.Max.X - 1 - right), Top: pt(top - r.Min.Y), Bottom: pt(r.Max.Y - 1 - bottom),
		Corner: math.Round(corner/scale*2) / 2, Colour: hexColour(img.At(left+int(math.Round(2*scale)), mid))}, true
}

// compareHighlights: the view's highlight sits where the menu's own does,
// has its corners (within half a point each: the corner estimate is
// rounded to that, review B3-m2) and its colour (within 24 over the three
// channels, B3-m3) — or says that it could not be measured.
func compareHighlights(native, view *Shape) []string {
	if native == nil || view == nil {
		return []string{fmt.Sprintf("the highlights could not both be measured: native %s, view %s", native, view)}
	}
	var p []string
	for _, d := range []struct {
		what   string
		n, v   float64
		within float64
	}{{"left inset", native.Left, view.Left, 0.5}, {"right inset", native.Right, view.Right, 0.5}, {"top inset", native.Top, view.Top, 0.5},
		{"bottom inset", native.Bottom, view.Bottom, 0.5}, {"corner", native.Corner, view.Corner, 0.5}} {
		if math.Abs(d.n-d.v) > d.within {
			p = append(p, fmt.Sprintf("the view's highlight %s is %.1f pt, the menu's own %.1f", d.what, d.v, d.n))
		}
	}
	if d := colourDist(native.Colour, view.Colour); d > 24 || native.Colour == "" || view.Colour == "" {
		p = append(p, fmt.Sprintf("the view's highlight is %s, the menu's own %s", view.Colour, native.Colour))
	}
	return p
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
