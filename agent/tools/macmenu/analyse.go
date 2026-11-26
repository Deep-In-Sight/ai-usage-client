// Command macmenu is a CI tool (macOS runners only; it refuses to run
// anywhere else): it opens a tray helper's menu-bar menu as a person
// would (a click on the status item, then the pointer and the keys, all
// posted as events), reads the open menu through the accessibility
// interface, captures it with screencapture, and judges the usage panel's
// view items (plan Task 9): their sizes and width, their VoiceOver labels,
// the bars in the capture, a click on a row, Return on a highlighted row
// and Escape. It prints one JSON object (the measured facts and the
// problems found). This file is the part that is plain Go: what is
// expected and how it is judged, tested on every OS.
package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
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
	// what the helper sent (its click messages) after each step; "" none
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
	Escape       string `json:"escape"`
	EscapeClosed bool   `json:"escape_closed"`
	// Return in the menu of the active helper: "sent=ID closed=BOOL", or
	// "not active …" when it could not be made the active app
	Active   string   `json:"active_return"`
	Errors   []string `json:"errors,omitempty"`
	Problems []string `json:"problems"`
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

// viewLabel is what VoiceOver gets for a view item: its view's label (or
// the item's own description, should the menu item stand for its view).
func viewLabel(it Item) string {
	if it.ChildLabel != "" {
		return it.ChildLabel
	}
	return it.Label
}

// viewSize is a view item's size: its view's own, else the menu item's.
func viewSize(it Item) (w, h float64) {
	if it.ChildW > 0 && it.ChildH > 0 {
		return it.ChildW, it.ChildH
	}
	return it.Rect.W, it.Rect.H
}

// viewRole is the role of what says that label.
func viewRole(it Item) string {
	if it.ChildLabel != "" {
		return it.ChildRole
	}
	return it.Role
}

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
	m := panel.MetricsAt(96)
	widest := 0.0
	for _, it := range f.Items {
		widest = math.Max(widest, it.Rect.W)
	}
	for _, w := range want {
		it, ok := byID(f.Items, w.ID)
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
		vw, vh := viewSize(it)
		if vw < float64(m.Width) || math.Abs(vw-widest) > 0.5 {
			p = append(p, fmt.Sprintf("%s: %.0f pt wide, want the menu's %.0f (at least %d)", w.ID, vw, widest, m.Width))
		}
		var h float64
		switch w.Kind {
		case "header":
			h = float64(m.Header)
		case "row":
			h = float64(m.Row)
		}
		if h > 0 && math.Abs(vh-h) > 0.5 {
			p = append(p, fmt.Sprintf("%s: %.0f pt high, want %.0f", w.ID, vh, h))
		}
		if h == 0 && vh < float64(m.Item)-0.5 {
			p = append(p, fmt.Sprintf("%s: %.0f pt high, want at least %d", w.ID, vh, m.Item))
		}
		if w.Kind == "header" && it.ChildEnabled {
			p = append(p, fmt.Sprintf("%s: a header can be clicked", w.ID))
		}
	}
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
	if f.DownSelected != "" && f.DownSelected != "r.claude.0" {
		p = append(p, fmt.Sprintf("Down chose %q, want r.claude.0", f.DownSelected))
	}
	if f.Return != "r.claude.0" {
		p = append(p, fmt.Sprintf("Return on the highlighted 5-hour sent %q, want r.claude.0", f.Return))
	}
	if !f.ReturnClosed {
		p = append(p, "the menu stayed open after Return")
	}
	switch {
	case f.Active == "" || strings.HasPrefix(f.Active, "not active"):
		// not judged: the CI step says so (not measurable on the runner)
	case f.Active != "sent=r.claude.0 closed=true":
		p = append(p, fmt.Sprintf("Return on the highlighted 5-hour in the active helper's menu: %s, want sent=r.claude.0 closed=true", f.Active))
	}
	if f.Escape != "" {
		p = append(p, fmt.Sprintf("Escape sent %q, want nothing", f.Escape))
	}
	if !f.EscapeClosed {
		p = append(p, "the menu stayed open after Escape")
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

// helloAndView are the lines a helper of the fixture gets: its hello and
// the fixture's view (the menu, the panel, the icon and its title).
func helloAndView(token string, in tray.MenuInputs) []trayproto.Msg {
	p := tray.BuildPanel(in)
	pct := 42.0
	return []trayproto.Msg{
		{V: trayproto.Version, Type: trayproto.TypeHello, Token: token, Provider: "claude"},
		{V: trayproto.Version, Type: trayproto.TypeView, Items: tray.BuildMenu(in), Panel: &p,
			Icon: &trayproto.Icon{Provider: "claude", Percent: &pct, Level: "normal"}, Title: "42%", Tooltip: "AI Usage"},
	}
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
	fmt.Fprintf(&b, "; picture %d px; header click %s (stayed open %v); click %s (closed %v); Down chose %s, highlight %s on %s; Return %s (closed %v); Escape %s (closed %v)",
		f.Picture, orNone(f.HeaderClick), f.HeaderStayed, orNone(f.Click), f.ClickClosed, orNone(f.DownSelected), orNone(f.Highlight), orNone(f.HeaderBg),
		orNone(f.Return), f.ReturnClosed, orNone(f.Escape), f.EscapeClosed)
	fmt.Fprintf(&b, "; Return in the active helper's menu: %s", orNone(f.Active))
	fmt.Fprintf(&b, "; highlight native %s, view %s", f.NativeHL, f.ViewHL)
	fmt.Fprintf(&b, "; windows %s (menu window seen %v)", orNone(f.Windows), f.WinSeen)
	if it, ok := byID(f.Items, "r.claude.0"); ok {
		fmt.Fprintf(&b, "; VoiceOver 5-hour: %s %q", viewRole(it), viewLabel(it))
	}
	return b.String()
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

// compareHighlights: the view's highlight sits where the menu's own does
// and has its corners (within a point each) — or says that it could not
// be measured.
func compareHighlights(native, view *Shape) []string {
	if native == nil || view == nil {
		return []string{fmt.Sprintf("the highlights could not both be measured: native %s, view %s", native, view)}
	}
	var p []string
	for _, d := range []struct {
		what   string
		n, v   float64
		within float64
	}{{"left inset", native.Left, view.Left, 1}, {"right inset", native.Right, view.Right, 1}, {"top inset", native.Top, view.Top, 1},
		{"bottom inset", native.Bottom, view.Bottom, 1}, {"corner", native.Corner, view.Corner, 1}} {
		if math.Abs(d.n-d.v) > d.within {
			p = append(p, fmt.Sprintf("the view's highlight %s is %.0f pt, the menu's own %.0f", d.what, d.v, d.n))
		}
	}
	return p
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
