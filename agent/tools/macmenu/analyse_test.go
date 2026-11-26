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
		Active: "sent=r.claude.0 closed=true", HeaderStayed: true, Click: "r.claude.0", ClickClosed: true, DownSelected: "r.claude.0", Return: "r.claude.0", ReturnClosed: true, EscapeClosed: true,
		NativeHL: &Shape{Left: 5, Right: 5, Corner: 3, Colour: "#0064e1"}, ViewHL: &Shape{Left: 5, Right: 5, Corner: 3, Colour: "#0064e1"},
		Highlight: "#0064e1", HeaderBg: "#ececec", Picture: 60, Bars: map[string][2]int{"r.claude.0": {120, 121}, "r.codex.0": {304, 304}}}
	y := 30.0
	for _, w := range want {
		h := 22.0
		it := Item{Role: "AXMenuItem", Enabled: true, Title: w.Title}
		if w.View {
			it.ChildRole, it.ChildLabel = "AXMenuItem", w.Label
			switch w.Kind {
			case "header":
				h, it.Enabled = 32, false
			case "row":
				h = 64
			default:
				h, it.Enabled = 30, false
			}
		}
		it.ChildEnabled = w.View && it.Enabled
		it.Rect = Rect{105, y, 336, h}
		y += h
		f.Items = append(f.Items, it)
	}
	f.Items = append(f.Items, Item{Role: "AXMenuItem", Rect: Rect{105, y, 336, 11}}) // a separator
	match(f.Items, want)
	return f, want
}

func TestJudgeAGoodRun(t *testing.T) {
	f, want := good()
	if p := judge(f, want); len(p) != 0 {
		t.Fatal(p)
	}
	f.Active = "not active (sent=none closed=false)" // not measurable: not judged
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
		"not trusted":    {func(f *Facts) { f.AXTrusted = false }, "not trusted"},
		"not opened":     {func(f *Facts) { f.Opened = "" }, "did not open"},
		"no view":        {func(f *Facts) { f.Items[1].ChildLabel, f.Items[1].ID = "", "" }, "r.claude.0: no menu item whose view says"},
		"text role":      {func(f *Facts) { f.Items[1].ChildRole = "AXStaticText" }, "role is \"AXStaticText\""},
		"narrow":         {func(f *Facts) { f.Items[1].Rect.W = 300 }, "r.claude.0: 300 pt wide"},
		"not the menu's": {func(f *Facts) { f.Items[0].Rect.W = 500 }, "want the menu's 500"},
		"row height":     {func(f *Facts) { f.Items[1].Rect.H = 22 }, "r.claude.0: 22 pt high, want 64"},
		"header height":  {func(f *Facts) { f.Items[0].Rect.H = 40 }, "h.claude: 40 pt high, want 32"},
		"server low":     {func(f *Facts) { f.Items[7].Rect.H = 20 }, "server: 20 pt high, want at least 30"},
		"header clicks":  {func(f *Facts) { f.Items[0].ChildEnabled = true }, "a header can be clicked"},
		"header sends":   {func(f *Facts) { f.HeaderClick = "h.claude" }, "a click on Claude's header sent \"h.claude\""},
		"header closes":  {func(f *Facts) { f.HeaderStayed = false }, "the menu closed on a click on Claude's header"},
		"inset off":      {func(f *Facts) { f.ViewHL.Left = 9 }, "highlight left inset is 9 pt, the menu's own 5"},
		"corner off":     {func(f *Facts) { f.ViewHL.Corner = 0 }, "highlight corner is 0 pt, the menu's own 3"},
		"no native":      {func(f *Facts) { f.NativeHL = nil }, "could not both be measured: native none"},
		"return open":    {func(f *Facts) { f.ReturnClosed = false }, "stayed open after Return"},
		"active return":  {func(f *Facts) { f.Active = "sent=none closed=false" }, "in the active helper's menu: sent=none closed=false"},
		"action is view": {func(f *Facts) { f.Items[8].ChildLabel = "Open dashboard" }, "the menu's own item has a view"},
		"bar short":      {func(f *Facts) { f.Bars["r.claude.0"] = [2]int{60, 121} }, "r.claude.0: the bar's colour runs 60 px"},
		"no picture":     {func(f *Facts) { f.Picture = 0 }, "picture"},
		"no highlight":   {func(f *Facts) { f.Highlight = f.HeaderBg }, "not highlighted"},
		"no click":       {func(f *Facts) { f.Click = "" }, "a click on 5-hour sent \"\""},
		"stays open":     {func(f *Facts) { f.ClickClosed = false }, "stayed open after the click"},
		"down elsewhere": {func(f *Facts) { f.DownSelected = "dashboard" }, "Down chose"},
		"no return":      {func(f *Facts) { f.Return = "" }, "Return on the highlighted 5-hour sent \"\""},
		"escape selects": {func(f *Facts) { f.Escape = "r.claude.0" }, "Escape sent"},
		"escape open":    {func(f *Facts) { f.EscapeClosed = false }, "stayed open after Escape"},
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
	for i := range f.Items {
		f.Items[i].ChildLabel = ""
	}
	if p := strings.Join(judgeAgent(f), "; "); !strings.Contains(p, "0 headers and 0 rows") {
		t.Fatal(p)
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
	analyse(&f, img, want)
	if f.Bars["r.claude.0"] != [2]int{128, 128} || f.Bars["r.codex.0"] != [2]int{304, 304} || f.Picture != 18 {
		t.Fatalf("%v picture %d", f.Bars, f.Picture)
	}
	if hexColour(panel.Normal) != "#d97757" || colourDist("#000000", "#0a0b0c") != 33 {
		t.Fatal("hex")
	}
}

func TestSummary(t *testing.T) {
	f, _ := good()
	if s := summary(f, true); !strings.Contains(s, "Escape closed the menu true") || strings.Contains(s, "header click") {
		t.Fatal(s)
	}
	s := summary(f, false)
	for _, want := range []string{"opened by click", "r.claude.0* 336x64", "r.claude.0 bar 120/121 px", "click r.claude.0 (closed true)", "Return r.claude.0", "Escape none (closed true)", "VoiceOver 5-hour: AXMenuItem \"5-hour, 42%"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q lacks %q", s, want)
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
	if p := compareHighlights(&Shape{Left: 5, Right: 5, Corner: 4}, &Shape{Left: 6, Right: 4, Top: 1, Corner: 3}); len(p) != 0 {
		t.Fatal(p)
	}
	if p := strings.Join(compareHighlights(&Shape{Left: 5, Right: 5, Top: 3, Bottom: 3, Corner: 4}, &Shape{Left: 5, Right: 5, Corner: 4}), "; "); !strings.Contains(p, "top inset is 0 pt, the menu's own 3") || !strings.Contains(p, "bottom inset") {
		t.Fatal(p)
	}
}
