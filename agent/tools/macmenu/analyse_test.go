package main

import (
	"image"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
)

func TestAllowedOnlyOnARunner(t *testing.T) {
	for env, want := range map[string]bool{"": false, "GITHUB_ACTIONS=true": false, "RUNNER_TEMP=/r": false, "GITHUB_ACTIONS=1 RUNNER_TEMP=/r": false, "GITHUB_ACTIONS=true RUNNER_TEMP=/r": true} {
		m := map[string]string{}
		for _, kv := range strings.Fields(env) {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		if got := allowed(func(k string) string { return m[k] }); got != want {
			t.Fatalf("%q: %v", env, got)
		}
	}
}

// The fixture's menu as the macOS helper shows it: the views in order,
// then the menu's own items.
func TestExpected(t *testing.T) {
	var ids []string
	for _, e := range expected(panelfixture.Combined()) {
		v := ""
		if e.View {
			v = "*"
		}
		ids = append(ids, e.ID+v)
	}
	if got := strings.Join(ids, " "); got != "h.claude* r.claude.0* r.claude.1* r.claude.2* h.codex* r.codex.0* r.codex.1* server* dashboard refresh autostart remove quit" {
		t.Fatal(got)
	}
	e := expected(panelfixture.Combined())
	if e[1].Label != panel.AccessibleText(e[1].Item) || !strings.HasPrefix(e[1].Label, "5-hour, 42%") {
		t.Fatalf("%+v", e[1])
	}
}

// good is a run that measured everything as it should be.
func good() (Facts, []Expected) {
	want := expected(panelfixture.Combined())
	f := Facts{AXTrusted: true, PostEvents: true, ScreenCapture: true, Opened: "click", Menu: Rect{100, 25, 336, 600}, Scale: 1,
		Active: "sent=r.claude.0 closed=true", ActiveSwitch: true, HeaderStayed: true, Click: "r.claude.0", ClickClosed: true, DownSelected: "r.claude.0", Return: "r.claude.0", ReturnClosed: true, EscapeClosed: true,
		AXOpen: "r.claude.0", AXOpenClosed: true, AXClosedThere: true, AXErrors: "0,-25205 then -25205",
		Down2: "r.claude.1", Down2Closed: true, UpSelected: "r.codex.1", Up: "r.codex.1", UpClosed: true, Pointer: "r.claude.1", PointerClosed: true,
		NativeHL: &Shape{Left: 5, Right: 5, Corner: 3, Colour: "#0064e1"}, ViewHL: &Shape{Left: 5, Right: 5, Corner: 3, Colour: "#0064e1"},
		Highlight: "#0064e1", HeaderBg: "#ececec", Picture: 60, Bars: map[string][2]int{"r.claude.0": {120, 121}, "r.codex.0": {304, 304}}}
	f.Items = menuItems(want)
	f.Reopens = []Reopen{
		{Fixture: "states", Opened: true, Menu: f.Menu, Scale: 1, Items: menuItems(expected(panelfixture.States())), Problem: 40, Stale: [2]int{120, 121}},
		{Fixture: "combined", Opened: true, Menu: f.Menu, Scale: 1, Items: menuItems(want)},
	}
	return f, want
}

// menuItems are want's items as the accessibility interface shows them in
// a good run (a header with a problem line 60 pt, the server line 48).
func menuItems(want []Expected) []Item {
	var out []Item
	y := 30.0
	for _, w := range want {
		h := 22.0
		it := Item{Role: "AXMenuItem", Enabled: true, Title: w.Title}
		if w.View {
			it.ChildRole, it.ChildLabel, it.Title = "AXMenuItem", w.Label, w.Label
			switch {
			case w.Kind == "header" && w.Item.Problem != "":
				h, it.Enabled = 60, false
			case w.Kind == "header":
				h, it.Enabled = 32, false
			case w.Kind == "row":
				h = 64
			default:
				h, it.Enabled = 48, false
			}
		}
		it.ChildEnabled = w.View && it.Enabled
		it.Rect = Rect{105, y, 336, h}
		if w.View {
			it.ChildW, it.ChildH = 336, h
		}
		y += h
		out = append(out, it)
	}
	out = append(out, Item{Role: "AXMenuItem", Rect: Rect{105, y, 336, 11}}) // a separator
	match(out, want)
	return out
}

func TestJudgeAGoodRun(t *testing.T) {
	f, want := good()
	if p := judge(f, want); len(p) != 0 {
		t.Fatal(p)
	}
	f.Active = "not active (sent=none closed=false)" // activation tried and failed: not judged
	if p := judge(f, want); len(p) != 0 {
		t.Fatal(p)
	}
	f, want = good()
	f.SpaceNative, f.SpaceNativeClosed, f.SpaceView, f.SpaceViewClosed = "dashboard", true, "r.claude.0", true // Space chooses
	if p := judge(f, want); len(p) != 0 {
		t.Fatal(p)
	}
}

// Each fault a run can find is reported.
func TestJudgeFindsEachFault(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*Facts)
		want   string
	}{
		"not trusted": {func(f *Facts) { f.AXTrusted = false }, "not trusted"},
		"not opened":  {func(f *Facts) { f.Opened = "" }, "did not open"},
		"no view":     {func(f *Facts) { f.Items[1].ChildLabel, f.Items[1].ID = "", "" }, "r.claude.0: no menu item whose view says"},
		"text role":   {func(f *Facts) { f.Items[1].ChildRole = "AXStaticText" }, "role is \"AXStaticText\""},
		"narrow":      {func(f *Facts) { f.Items[1].Rect.W, f.Items[1].ChildW = 300, 300 }, "r.claude.0: 300 pt wide"},
		// review B3-I5: the view's own size or label unread is a problem,
		// not the menu item's (which has the menu's width whatever the view's)
		"view size unread": {func(f *Facts) { f.Items[0].ChildW, f.Items[0].ChildH = 0, 0 }, "h.claude: its view's own size could not be read"},
		"view label unread": {func(f *Facts) {
			f.Items[1].Label, f.Items[1].ChildLabel, f.Items[1].ID = f.Items[1].ChildLabel, "", ""
			match(f.Items, expected(panelfixture.Combined()))
		}, "r.claude.0: no menu item whose view says"},
		"bar in title":   {func(f *Facts) { f.Items[1].Title = "████░░░░░░ 42%" }, "r.claude.0: its menu item's title"},
		"own item gone":  {func(f *Facts) { f.Items[12].Title, f.Items[12].ID = "", "" }, "quit: no menu item \"Quit\""},
		"errors":         {func(f *Facts) { f.Errors = []string{"screencapture: timed out"} }, "screencapture: timed out"},
		"no events":      {func(f *Facts) { f.PostEvents = false }, "may not post events"},
		"bar 10 short":   {func(f *Facts) { f.Bars["r.claude.0"] = [2]int{111, 121} }, "r.claude.0: the bar's colour runs 111 px"},
		"down nothing":   {func(f *Facts) { f.DownSelected = "" }, "Down chose \"\""},
		"doubled return": {func(f *Facts) { f.Return = "r.claude.0,r.claude.0" }, "Return on the highlighted 5-hour sent \"r.claude.0,r.claude.0\""},
		"doubled click":  {func(f *Facts) { f.Click = "r.claude.0,r.claude.0" }, "a click on 5-hour sent \"r.claude.0,r.claude.0\""},
		"down2":          {func(f *Facts) { f.Down2 = "" }, "Down, Down, Return sent \"\", want r.claude.1"},
		"down2 open":     {func(f *Facts) { f.Down2Closed = false }, "stayed open after Down, Down, Return"},
		"up":             {func(f *Facts) { f.Up = "" }, "Up from Open dashboard, Return sent \"\", want r.codex.1"},
		"pointer":        {func(f *Facts) { f.Pointer = "r.claude.0" }, "Return on Weekly under the pointer sent \"r.claude.0\""},
		"space unlike":   {func(f *Facts) { f.SpaceNative, f.SpaceNativeClosed = "dashboard", true }, "Space chooses the menu's own items, but on the highlighted 5-hour it sent \"\""},
		"space acts":     {func(f *Facts) { f.SpaceView = "r.claude.0" }, "Space does nothing on the menu's own items, but"},
		"active empty":   {func(f *Facts) { f.Active = "" }, "Return in the active helper's menu was not measured"},
		"active doubled": {func(f *Facts) { f.Active = "sent=r.claude.0,r.claude.0 closed=true" }, "in the active helper's menu: sent=r.claude.0,r.claude.0"},
		"no switch":      {func(f *Facts) { f.ActiveSwitch = false }, "did not say it took the test switch"},
		"colour off":     {func(f *Facts) { f.ViewHL.Colour = "#3070ff" }, "the view's highlight is #3070ff"},
		"no reopen":      {func(f *Facts) { f.Reopens = f.Reopens[1:] }, "not opened again after the states view"},
		"reopen closed":  {func(f *Facts) { f.Reopens[1].Opened = false }, "after the combined view: the menu did not open"},
		"header kept":    {func(f *Facts) { f.Reopens[0].Items[4].ChildH = 32 }, "after the states view: h.codex: 32 pt high, want at least 60"},
		"header grown":   {func(f *Facts) { f.Reopens[1].Items[4].ChildH = 60 }, "after the combined view: h.codex: 60 pt high, want 32"},
		"no problem px":  {func(f *Facts) { f.Reopens[0].Problem = 0 }, "0 pixels of the problem colour"},
		"not faded":      {func(f *Facts) { f.Reopens[0].Stale = [2]int{0, 121} }, "Claude's 5-hour bar runs 0 px in its faded colour, want 121"},
		"old label": {func(f *Facts) {
			f.Reopens[0].Items[1].ChildLabel, f.Reopens[0].Items[1].ID = f.Items[1].ChildLabel, ""
		}, "after the states view: r.claude.0: no menu item whose view says"},
		"narrow view":     {func(f *Facts) { f.Items[0].ChildW = 316 }, "h.claude: 316 pt wide, want the menu's 336"},
		"view low":        {func(f *Facts) { f.Items[0].ChildH = 36 }, "h.claude: 36 pt high, want 32"},
		"not the menu's":  {func(f *Facts) { f.Items[0].Rect.W = 500 }, "want the menu's 500"},
		"row height":      {func(f *Facts) { f.Items[1].Rect.H, f.Items[1].ChildH = 22, 22 }, "r.claude.0: 22 pt high, want 64"},
		"header height":   {func(f *Facts) { f.Items[0].Rect.H, f.Items[0].ChildH = 40, 40 }, "h.claude: 40 pt high, want 32"},
		"server low":      {func(f *Facts) { f.Items[7].Rect.H, f.Items[7].ChildH = 20, 20 }, "server: 20 pt high, want at least 42"},
		"server one line": {func(f *Facts) { f.Items[7].Rect.H, f.Items[7].ChildH = 30, 30 }, "server: 30 pt high, want at least 42 (two lines)"},
		"header clicks":   {func(f *Facts) { f.Items[0].ChildEnabled = true }, "a header can be clicked"},
		"header sends":    {func(f *Facts) { f.HeaderClick = "h.claude" }, "a click on Claude's header sent \"h.claude\""},
		"header closes":   {func(f *Facts) { f.HeaderStayed = false }, "the menu closed on a click on Claude's header"},
		"inset off":       {func(f *Facts) { f.ViewHL.Left = 9 }, "highlight left inset is 9.0 pt, the menu's own 5.0"},
		"corner off":      {func(f *Facts) { f.ViewHL.Corner = 2 }, "highlight corner is 2.0 pt, the menu's own 3.0"},
		"no native":       {func(f *Facts) { f.NativeHL = nil }, "could not both be measured: native none"},
		"return open":     {func(f *Facts) { f.ReturnClosed = false }, "stayed open after Return"},
		"active return":   {func(f *Facts) { f.Active = "sent=none closed=false" }, "in the active helper's menu: sent=none closed=false"},
		"action is view":  {func(f *Facts) { f.Items[8].ChildLabel = "Open dashboard" }, "the menu's own item has a view"},
		"bar short":       {func(f *Facts) { f.Bars["r.claude.0"] = [2]int{60, 121} }, "r.claude.0: the bar's colour runs 60 px"},
		"no picture":      {func(f *Facts) { f.Picture = 0 }, "picture"},
		"no highlight":    {func(f *Facts) { f.Highlight = f.HeaderBg }, "not highlighted"},
		"no click":        {func(f *Facts) { f.Click = "" }, "a click on 5-hour sent \"\""},
		"stays open":      {func(f *Facts) { f.ClickClosed = false }, "stayed open after the click"},
		"down elsewhere":  {func(f *Facts) { f.DownSelected = "dashboard" }, "Down chose"},
		"no return":       {func(f *Facts) { f.Return = "" }, "Return on the highlighted 5-hour sent \"\""},
		"escape selects":  {func(f *Facts) { f.Escape = "r.claude.0" }, "Escape sent"},
		"escape open":     {func(f *Facts) { f.EscapeClosed = false }, "stayed open after Escape"},
		// review N4: one action per open, none from a closed menu
		"ax doubled":   {func(f *Facts) { f.AXOpen = "r.claude.0,r.claude.0" }, "two VoiceOver presses on 5-hour in the open menu sent \"r.claude.0,r.claude.0\", want r.claude.0 once"},
		"ax nothing":   {func(f *Facts) { f.AXOpen = "" }, "two VoiceOver presses on 5-hour in the open menu sent \"\""},
		"ax open":      {func(f *Facts) { f.AXOpenClosed = false }, "stayed open after a VoiceOver press"},
		"ax on closed": {func(f *Facts) { f.AXClosed = "r.claude.0" }, "a VoiceOver press on 5-hour of the closed menu sent \"r.claude.0\", want nothing"},
	} {
		f, want := good()
		c.change(&f)
		if p := strings.Join(judge(f, want), "; "); !strings.Contains(p, c.want) {
			t.Fatalf("%s: %q does not say %q", name, p, c.want)
		}
	}
}

func TestJudgeAgent(t *testing.T) {
	f, _ := good()
	if p := judgeAgent(f); len(p) != 0 {
		t.Fatal(p)
	}
	for name, c := range map[string]struct {
		change func(*Facts)
		want   string
	}{
		"no views":    {func(f *Facts) { f.Items = f.Items[8:] }, "0 headers and 0 rows"},
		"narrow":      {func(f *Facts) { f.Items[1].ChildW = 300 }, "300 pt wide, want the menu's 336"},
		"size unread": {func(f *Facts) { f.Items[1].ChildW, f.Items[1].ChildH = 0, 0 }, "its view's own size could not be read"},
		"bar title":   {func(f *Facts) { f.Items[1].Title = "████░░░░░░ 42%" }, "its menu item's title has the text menu's bar"},
		"errors":      {func(f *Facts) { f.Errors = []string{"no status item"} }, "no status item"},
		"not opened":  {func(f *Facts) { f.Opened = "" }, "the menu did not open"},
	} {
		f, _ := good()
		c.change(&f)
		if p := strings.Join(judgeAgent(f), "; "); !strings.Contains(p, c.want) {
			t.Fatalf("%s: %q does not say %q", name, p, c.want)
		}
	}
}

// The capture's bars and picture are read from the pixels.
func TestAnalyse(t *testing.T) {
	f, want := good()
	img := image.NewNRGBA(image.Rect(0, 0, 336, 700))
	for i := range img.Pix {
		img.Pix[i] = 0xec
	}
	put := func(id string, from, n int, c color.NRGBA) {
		it, _ := byID(f.Items, id)
		r := inPicture(it.Rect, f.Menu, 1)
		for x := r.Min.X + from; x < r.Min.X+from+n; x++ {
			img.SetNRGBA(x, r.Min.Y+30, c)
		}
	}
	put("r.claude.0", 16, 128, panel.Normal)
	put("r.codex.0", 16, 304, panel.Critical)
	put("h.claude", 16, 18, panel.Normal)
	put("h.claude", 200, 30, panel.Normal) // past the picture: not counted
	analyse(&f, img, want)
	if f.Bars["r.claude.0"] != [2]int{128, 128} || f.Bars["r.codex.0"] != [2]int{304, 304} || f.Picture != 18 {
		t.Fatalf("%v picture %d", f.Bars, f.Picture)
	}

	// the States view: the problem colour under Codex's header line (not
	// in it), Claude's bar faded over the menu's background
	r := f.Reopens[0]
	img = image.NewNRGBA(image.Rect(0, 0, 336, 700))
	for i := range img.Pix {
		img.Pix[i] = 0xec
	}
	at := func(id string, x, y, n int, c color.NRGBA) {
		it, _ := byID(r.Items, id)
		rc := inPicture(it.Rect, r.Menu, 1)
		for i := 0; i < n; i++ {
			img.SetNRGBA(rc.Min.X+x+i, rc.Min.Y+y, c)
		}
	}
	at("h.codex", 16, 10, 50, panel.ProblemOnLight) // in the header line: not counted
	at("h.codex", 16, 40, 20, panel.ProblemOnLight)
	bg := color.NRGBA{0xec, 0xec, 0xec, 0xff}
	at("r.claude.0", 16, 30, 128, panel.Fade(panel.Normal, bg, panel.StaleOpacity))
	at("r.claude.0", 16, 31, 200, panel.Normal) // the unfaded colour: not counted
	analyseStates(&r, img)
	if r.Problem != 20 || r.Stale != [2]int{128, 128} {
		t.Fatalf("problem %d stale %v", r.Problem, r.Stale)
	}
	if hexColour(panel.Normal) != "#d97757" || colourDist("#000000", "#0a0b0c") != 33 {
		t.Fatal("hex")
	}
}

func TestSummary(t *testing.T) {
	f, _ := good()
	if s := summary(f, true); !strings.Contains(s, "Escape closed the menu true") || strings.Contains(s, "picture") {
		t.Fatal(s)
	}
	s := summary(f, false)
	for _, want := range []string{"opened by click", "r.claude.0* 336x64", "r.claude.0 bar 120/121 px", "picture 60 px", "VoiceOver 5-hour: AXMenuItem \"5-hour, 42%",
		"after the states view: menu 336x600, h.codex 336x60, problem colour 40 px, Claude's faded bar 120/121 px", "after the combined view: menu 336x600, h.codex 336x32"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q lacks %q", s, want)
		}
	}
	k := keys(f)
	for _, want := range []string{"click r.claude.0 (closed true)", "Return r.claude.0", "Escape none (closed true)", "Down Down Return r.claude.1 (closed true)",
		"Up from Open dashboard chose r.codex.1, Return r.codex.1", "Return in the active helper's menu: sent=r.claude.0 closed=true (switch taken true)",
		"VoiceOver press twice on 5-hour r.claude.0 (closed true), on the closed menu's none (its view there true; AXError 0,-25205 then -25205)"} {
		if !strings.Contains(k, want) {
			t.Fatalf("%q lacks %q", k, want)
		}
	}
}

// A highlight's place is read from the pixels: a rounded fill inset from
// the item's sides, measured the same way for the menu's own and the
// view's.
func TestHighlightShape(t *testing.T) {
	bg, hl := color.NRGBA{0xec, 0xec, 0xec, 0xff}, color.NRGBA{0x00, 0x64, 0xe1, 0xff}
	for _, c := range []struct {
		scale                  int
		inX, inY, radius, w, h int
	}{{1, 5, 0, 4, 320, 64}, {2, 5, 1, 6, 300, 24}} {
		s := c.scale
		img := image.NewNRGBA(image.Rect(0, 0, (c.w+20)*s, (c.h+20)*s))
		for i := range img.Pix {
			img.Pix[i] = 0xec
		}
		item := image.Rect(10*s, 10*s, (10+c.w)*s, (10+c.h)*s)
		fill := image.Rect(item.Min.X+c.inX*s, item.Min.Y+c.inY*s, item.Max.X-c.inX*s, item.Max.Y-c.inY*s)
		r := float64(c.radius * s)
		for y := fill.Min.Y; y < fill.Max.Y; y++ {
			for x := fill.Min.X; x < fill.Max.X; x++ {
				// outside a corner's circle: not filled
				cx := math.Max(float64(fill.Min.X)+r-0.5-float64(x), float64(x)-(float64(fill.Max.X)-r-0.5))
				cy := math.Max(float64(fill.Min.Y)+r-0.5-float64(y), float64(y)-(float64(fill.Max.Y)-r-0.5))
				if cx > 0 && cy > 0 && cx*cx+cy*cy > r*r {
					continue
				}
				img.SetNRGBA(x, y, hl)
			}
		}
		img.SetNRGBA(item.Min.X+40*s, (item.Min.Y+item.Max.Y)/2, color.NRGBA{0xff, 0xff, 0xff, 0xff}) // text inside
		got, ok := highlightShape(img, item, bg, float64(s))
		want := Shape{Left: float64(c.inX), Right: float64(c.inX), Top: float64(c.inY), Bottom: float64(c.inY), Corner: got.Corner, Colour: "#0064e1"}
		if !ok || got != want || math.Abs(got.Corner-float64(c.radius)) > 0.5 {
			t.Fatalf("scale %d: %+v (%v), want %+v with a corner near %d", s, got, ok, want, c.radius)
		}
	}
	plain := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	for i := range plain.Pix {
		plain.Pix[i] = 0xec
	}
	if _, ok := highlightShape(plain, image.Rect(0, 0, 50, 30), bg, 1); ok {
		t.Fatal("measured a highlight in a picture without one")
	}
	if p := compareHighlights(&Shape{Left: 5, Right: 5, Corner: 4, Colour: "#0064e1"}, &Shape{Left: 5.5, Right: 4.5, Top: 0.5, Corner: 4.5, Colour: "#0a6ae8"}); len(p) != 0 {
		t.Fatal(p)
	}
	// review B3-m2: the radii 4 and 5 differ by more than the tolerance
	if p := strings.Join(compareHighlights(&Shape{Left: 5, Right: 5, Corner: 5, Colour: "#0064e1"}, &Shape{Left: 5, Right: 5, Corner: 4, Colour: "#0064e1"}), "; "); !strings.Contains(p, "corner is 4.0 pt, the menu's own 5.0") {
		t.Fatal(p)
	}
	if p := strings.Join(compareHighlights(&Shape{Left: 5, Right: 5, Top: 3, Bottom: 3, Corner: 4, Colour: "#0064e1"}, &Shape{Left: 5, Right: 5, Corner: 4, Colour: "#0064e1"}), "; "); !strings.Contains(p, "top inset is 0.0 pt, the menu's own 3.0") || !strings.Contains(p, "bottom inset") {
		t.Fatal(p)
	}
}
