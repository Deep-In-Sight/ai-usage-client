package panel

import (
	"encoding/json"
	"image/color"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func pf(v float64) *float64 { return &v }

// Global Constraint 7: the original extension's colours (design 5.2).
func TestColoursExact(t *testing.T) {
	for _, c := range []struct {
		got  color.NRGBA
		want uint32 // 0xRRGGBBAA
	}{
		{Normal, 0xd97757ff}, {Warning, 0xf5c211ff}, {Critical, 0xf66151ff},
		{Track, 0x80808052}, // rgba(128,128,128,0.32): 0.32 × 255 = 81.6 -> 82
		{ProblemOnDark, 0xf5c211ff}, {ProblemOnLight, 0x9a6700ff},
	} {
		got := uint32(c.got.R)<<24 | uint32(c.got.G)<<16 | uint32(c.got.B)<<8 | uint32(c.got.A)
		if got != c.want {
			t.Fatalf("%08x, want %08x", got, c.want)
		}
	}
	eq(t, StaleOpacity, 0.6)
	eq(t, DimMix, 0.45)
	eq(t, BarHeight, 8)
	for level, want := range map[string]color.NRGBA{"normal": Normal, "warning": Warning, "critical": Critical} {
		c, ok := BarColour(level)
		eq(t, ok, true)
		eq(t, c, want)
	}
	for _, level := range []string{"none", "", "other"} {
		if _, ok := BarColour(level); ok {
			t.Fatalf("a bar colour for %q", level)
		}
	}
}

// PR-18, one table for Go and the extension (pre-flight F12).
func TestFillWidth(t *testing.T) {
	b, err := os.ReadFile("testdata/fillwidth.json")
	if err != nil {
		t.Fatal(err)
	}
	type fillCase struct {
		Percent *float64
		Fill    int
	}
	var table struct {
		Track, Bar int
		Cases      []fillCase
	}
	if err := json.Unmarshal(b, &table); err != nil || len(table.Cases) < 8 {
		t.Fatalf("%v %+v", err, table)
	}
	for _, c := range table.Cases {
		if got := FillWidth(c.Percent, table.Track, table.Bar); got != c.Fill {
			t.Fatalf("FillWidth(%v) = %d, want %d", c.Percent, got, c.Fill)
		}
	}
	// a track shorter than the bar's height is never overfilled
	eq(t, FillWidth(pf(1), 6, 8), 6)
	eq(t, FillWidth(pf(-5), 300, 8), 0)
}

// bgra is pixel (x, y) of a BarPixels picture.
func bgra(b []byte, w, x, y int) [4]int {
	i := (y*w + x) * 4
	return [4]int{int(b[i]), int(b[i+1]), int(b[i+2]), int(b[i+3])}
}

func near(t *testing.T, what string, got, want [4]int) {
	t.Helper()
	for i := range got {
		if d := got[i] - want[i]; d < -1 || d > 1 {
			t.Fatalf("%s: BGRA %v, want %v ±1", what, got, want)
		}
	}
}

func TestBarPixels(t *testing.T) {
	const w, h = 300, 8
	normal := [4]int{0x57, 0x77, 0xd9, 255}
	track := [4]int{41, 41, 41, 82} // 0x80 × 82/255, premultiplied
	for _, fill := range []int{0, 8, 150, 300} {
		b := BarPixels(w, h, fill, Normal, Track)
		eq(t, len(b), w*h*4)
		for y := range h {
			for x := range w {
				p := bgra(b, w, x, y)
				if p[0] > p[3] || p[1] > p[3] || p[2] > p[3] {
					t.Fatalf("fill %d (%d,%d) %v: not premultiplied", fill, x, y, p)
				}
			}
		}
		// the rounding is the same at the top and the bottom (no drift
		// across rows, pre-flight F24)
		for x := range w {
			eq(t, bgra(b, w, x, 0), bgra(b, w, x, h-1))
			eq(t, bgra(b, w, x, 1), bgra(b, w, x, h-2))
		}
	}
	// no fill: track only, grey everywhere
	b := BarPixels(w, h, 0, Normal, Track)
	for y := range h {
		for x := range w {
			if p := bgra(b, w, x, y); p[0] != p[1] || p[1] != p[2] {
				t.Fatalf("(%d,%d) %v has a hue", x, y, p)
			}
		}
	}
	near(t, "track middle", bgra(b, w, 150, 4), track)
	// half: the fill's colour, then the track; both ends round
	b = BarPixels(w, h, 150, Normal, Track)
	near(t, "fill", bgra(b, w, 75, 4), normal)
	near(t, "track", bgra(b, w, 225, 4), track)
	if p := bgra(b, w, 0, 0); p[3] >= 128 {
		t.Fatalf("corner (0,0) %v: not rounded", p)
	}
	eq(t, bgra(b, w, 4, 4), normal)
	if p := bgra(b, w, 149, 0); p[3] >= 255 {
		t.Fatalf("(149,0) %v: the fill's end is not rounded", p)
	}
	if p := bgra(b, w, 148, 4); p != normal {
		t.Fatalf("(148,4) %v: the fill's end is cut short", p)
	}
	// full: rounded at the right too, and the same at both ends
	b = BarPixels(w, h, 300, Normal, Track)
	if p := bgra(b, w, 299, 0); p[3] >= 128 {
		t.Fatalf("corner (299,0) %v: not rounded", p)
	}
	for y := range h {
		for x := range w {
			eq(t, bgra(b, w, x, y), bgra(b, w, w-1-x, y))
		}
	}
	// a fill as short as the bar is high is a round dot
	b = BarPixels(w, h, 8, Critical, Track)
	if p := bgra(b, w, 4, 4); p != [4]int{0x51, 0x61, 0xf6, 255} {
		t.Fatalf("dot %v", p)
	}
	if p := bgra(b, w, 7, 0); p[2] > 0x80 {
		t.Fatalf("(7,0) %v: the dot is square", p)
	}
}

func TestDim(t *testing.T) {
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	got := Dim(white, color.NRGBA{0x2b, 0x2b, 0x2b, 0xff})
	for _, c := range []uint8{got.R, got.G, got.B} {
		if c < 0x9f || c > 0xa1 {
			t.Fatalf("%v, want #a0a0a0 ±1", got)
		}
	}
	eq(t, got.A, uint8(0xff))
	eq(t, Dim(color.NRGBA{0, 0, 0, 0xff}, white), color.NRGBA{115, 115, 115, 0xff}) // 0.45 × 255 = 114.75
}

func TestItemsFromFixture(t *testing.T) {
	items := Items(tray.BuildPanel(panelfixture.States()))
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	eq(t, keys, []string{"h.claude", "h.codex", "r.claude.0", "r.claude.1", "r.claude.2", "r.codex.0", "r.codex.1"})
	eq(t, items["h.codex"].Problem, text.CodexExpired)
	eq(t, items["h.claude"].Problem, "")
	for k, it := range items {
		if strings.Contains(k, ".claude") != it.Stale {
			t.Fatalf("%s stale %v", k, it.Stale)
		}
	}
}

func TestItemsCarryThePanel(t *testing.T) {
	in := panelfixture.Combined()
	c := in.Snap.Providers[provider.Codex]
	for i := range 4 {
		c.Rows = append(c.Rows, c.Rows[1])
		c.Rows[len(c.Rows)-1].Key += string(rune('a' + i))
	}
	m := map[provider.ID]status.ProviderStatus{provider.Claude: in.Snap.Providers[provider.Claude], provider.Codex: c}
	in.Snap.Providers = m
	items := Items(tray.BuildPanel(in))
	eq(t, items["h.claude"], Item{Kind: "header", Provider: "claude", Name: "Claude", Account: "team-a@… · 3 PCs", Updated: "Updated 18:52"})
	eq(t, items["r.claude.0"], Item{Kind: "row", Provider: "claude", Label: "5-hour", PercentText: "42%",
		Detail: "Resets Fri 21:20 · in 2h 20m", Level: "normal", Percent: pf(42)})
	eq(t, items["r.codex.0"].Level, "critical")
	eq(t, items["r.codex.4"], Item{Kind: "more", Provider: "codex", Label: "More limits on the dashboard", Level: "none"})
	eq(t, len(items), 2+3+5)
	// the JSON names the macOS view reads (snake_case)
	b, err := json.Marshal(items["r.claude.0"])
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var names []string
	for k := range v {
		names = append(names, k)
	}
	slices.Sort(names)
	eq(t, names, []string{"account", "detail", "kind", "label", "level", "name", "percent", "percent_text", "problem", "provider", "stale", "updated"})
}

func TestMetricsScale(t *testing.T) {
	for dpi, want := range map[int]Metrics{
		96:  {Width: 320, Header: 32, Problem: 28, Row: 64, Pad: 12, Bar: 8},
		120: {Width: 400, Header: 40, Problem: 35, Row: 80, Pad: 15, Bar: 10},
		144: {Width: 480, Header: 48, Problem: 42, Row: 96, Pad: 18, Bar: 12},
		192: {Width: 640, Header: 64, Problem: 56, Row: 128, Pad: 24, Bar: 16},
	} {
		if got := MetricsAt(dpi); got != want {
			t.Fatalf("MetricsAt(%d) = %+v, want %+v", dpi, got, want)
		}
	}
	eq(t, MetricsAt(0), MetricsAt(96)) // an unknown DPI is the standard one
	eq(t, MetricsAt(96).Bar, BarHeight)
}
