package panel_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel/panelcheck"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
)

// measurer is 7 px a character (the heavier faces 8), 16 px lines at 96
// DPI, scaled.
type measurer struct{ dpi int }

func (f measurer) s(v int) int { return (v*f.dpi + 48) / 96 }
func (f measurer) TextWidth(font panel.Font, s string) int {
	per := 7
	if font != panel.Regular {
		per = 8
	}
	return f.s(per * utf8.RuneCountInString(s))
}
func (f measurer) LineHeight(panel.Font) int { return f.s(16) }

var full = 100.0

// wholeItems are the fixtures' items and the longest texts a real menu
// gets: the longest account with the PCs and a plan, a wrapped problem,
// the server line naming two fetching PCs, a blocked limit's reset line,
// every action.
func wholeItems() map[string]panel.Item {
	out := map[string]panel.Item{
		"long account": {Kind: "header", Provider: "codex", Name: "Codex", Account: "firstname.lastname.…@… · 12 PCs · Enterprise", Updated: "Updated Mon 15:30"},
		"problem":      {Kind: "header", Provider: "codex", Name: "Codex", Problem: strings.Repeat("Sign-in expired: open Codex to renew it ", 3)},
		"two hosts":    {Kind: "info", Text: "Server connected · Claude from DESKTOP-ABCDEFG · Codex from DESKTOP-HIJKLMN", Dim: true},
		"prompt":       {Kind: "info", Text: "Sign in to Claude Code or Codex"},
		"checked":      {Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true},
		"long action":  {Kind: "action", Text: strings.Repeat("Remove AI Usage ", 4), Enabled: true},
		"blocked":      {Kind: "row", Provider: "claude", Label: "5-hour", PercentText: "100%", Detail: "Blocked · resets Fri 19:35 · in 35m", Level: "critical", Percent: &full},
	}
	for n, in := range map[string]tray.MenuInputs{"combined": panelfixture.Combined(), "states": panelfixture.States()} {
		p := tray.BuildPanel(in)
		for id, it := range panel.Items(p) {
			out[n+" "+id] = it
		}
		out[n+" server"] = panel.Item{Kind: "info", Text: p.Server, Dim: true}
		for _, a := range p.Actions {
			out[n+" "+a.ID] = panel.Item{Kind: "action", Text: a.Text, Enabled: a.Enabled, Checkbox: a.Checkbox, Checked: a.Checked}
		}
	}
	return out
}

// Review B-I1: at the size Size gives (and wider, as Windows widens an
// item), every text of every item is drawn whole: in a box as wide as the
// text and as high as its line, inside the item. The clamp in Layout
// stays as a guard; a step it had to cut fails here. Also with text half
// as large again as the DPI's (Windows' "Make text bigger" grows the menu
// font, not the DPI): a row widens the panel then, as a header does
// (review A-m6).
func TestLayoutDrawsEveryTextWhole(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		for _, text := range []int{dpi, dpi * 3 / 2} {
			m, tm := panel.MetricsAt(dpi), measurer{text}
			for name, it := range wholeItems() {
				w, h := panel.Size(it, m, tm)
				for _, extra := range []int{0, 43} {
					for _, sel := range []bool{false, true} {
						ops := panel.Layout(it, m, tm, w+extra, h, panel.DarkPalette, sel)
						if miss := panelcheck.Missing(it, ops, m, tm, w+extra, h); len(miss) > 0 {
							t.Fatalf("dpi %d, text at %d %s (%dx%d): %s", dpi, text, name, w+extra, h, strings.Join(miss, "; "))
						}
					}
				}
			}
		}
	}
}

// The check itself fails for a text cut short, a box lower than its line
// and a text dropped.
func TestMissingFindsCutText(t *testing.T) {
	m, tm := panel.MetricsAt(96), measurer{96}
	it := wholeItems()["combined r.claude.0"]
	w, h := panel.Size(it, m, tm)
	if miss := panelcheck.Missing(it, panel.Layout(it, m, tm, w, h-20, panel.LightPalette, false), m, tm, w, h-20); len(miss) == 0 {
		t.Fatal("a row 20 px too low shows every text")
	}
	hd := wholeItems()["long account"]
	if miss := panelcheck.Missing(hd, panel.Layout(hd, m, tm, 320, 32, panel.LightPalette, false), m, tm, 320, 32); len(miss) == 0 {
		t.Fatal("the longest account at 320 px is whole")
	}
	ops := panel.Layout(it, m, tm, w, h, panel.LightPalette, false)
	var kept []panel.Op
	for _, o := range ops {
		if o.Text != it.Detail {
			kept = append(kept, o)
		}
	}
	if miss := panelcheck.Missing(it, kept, m, tm, w, h); len(miss) != 1 || !strings.Contains(miss[0], "not drawn") {
		t.Fatalf("detail dropped: %v", miss)
	}
}

// Review N6: the check also fails for texts drawn over each other or over
// the bar, and for a row without its bar, a header without its picture, a
// checked action without its check mark.
func TestMissingFindsOverlapsAndDroppedPictures(t *testing.T) {
	m, tm := panel.MetricsAt(96), measurer{96}
	items := wholeItems()
	layout := func(it panel.Item) ([]panel.Op, int, int) {
		w, h := panel.Size(it, m, tm)
		return panel.Layout(it, m, tm, w, h, panel.LightPalette, false), w, h
	}
	without := func(ops []panel.Op, drop func(panel.Op) bool) []panel.Op {
		var out []panel.Op
		for _, o := range ops {
			if !drop(o) {
				out = append(out, o)
			}
		}
		return out
	}
	moved := func(ops []panel.Op, text string, y func(panel.Op) int) []panel.Op {
		out := append([]panel.Op(nil), ops...)
		for i := range out {
			if out[i].Kind == panel.Text && out[i].Text == text {
				out[i].Y = y(out[i])
			}
		}
		return out
	}
	row := items["combined r.claude.0"]
	ops, w, h := layout(row)
	if miss := panelcheck.Missing(row, ops, m, tm, w, h); len(miss) > 0 {
		t.Fatalf("the row as drawn: %v", miss)
	}
	var label, bar panel.Op
	for _, o := range ops {
		switch {
		case o.Kind == panel.Text && o.Text == row.Label:
			label = o
		case o.Kind == panel.Pixels:
			bar = o
		}
	}
	for name, c := range map[string]struct {
		it   panel.Item
		ops  []panel.Op
		want string
	}{
		"detail on the label's line": {row, moved(ops, row.Detail, func(panel.Op) int { return label.Y }), "overlap"},
		"detail over the bar":        {row, moved(ops, row.Detail, func(panel.Op) int { return bar.Y - 2 }), "overlap"},
		"no bar":                     {row, without(ops, func(o panel.Op) bool { return o.Kind == panel.Pixels }), "bar not drawn"},
	} {
		if miss := panelcheck.Missing(c.it, c.ops, m, tm, w, h); !strings.Contains(strings.Join(miss, "; "), c.want) {
			t.Errorf("%s: %v, want %q", name, miss, c.want)
		}
	}
	hd := items["combined h.claude"]
	hops, hw, hh := layout(hd)
	if miss := panelcheck.Missing(hd, hops, m, tm, hw, hh); len(miss) > 0 {
		t.Fatalf("the header as drawn: %v", miss)
	}
	if miss := panelcheck.Missing(hd, without(hops, func(o panel.Op) bool { return o.Kind == panel.Pixels }), m, tm, hw, hh); !strings.Contains(strings.Join(miss, "; "), "picture not drawn") {
		t.Errorf("header without its picture: %v", miss)
	}
	ck := items["checked"]
	cops, cw, ch := layout(ck)
	if miss := panelcheck.Missing(ck, cops, m, tm, cw, ch); len(miss) > 0 {
		t.Fatalf("the checked action as drawn: %v", miss)
	}
	if miss := panelcheck.Missing(ck, without(cops, func(o panel.Op) bool { return o.Kind == panel.Pixels }), m, tm, cw, ch); !strings.Contains(strings.Join(miss, "; "), "check mark not drawn") {
		t.Errorf("checked action without its mark: %v", miss)
	}
}

// Review A-I2: in the usual team case (the server line names the PCs that
// fetch) and with every fixture, every item but the headers measures the
// panel's width at every DPI: the panel keeps 320 px at 96 DPI. (The
// headers' lines are wider with this stand-in font than with the menu
// font; trayhelper's TestMeasureMatchesDrawAtEveryDPI measures them with
// the real one.)
func TestPanelKeepsItsWidth(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		m, tm := panel.MetricsAt(dpi), measurer{dpi}
		for name, it := range wholeItems() {
			if it.Kind == "header" || name == "long action" {
				continue
			}
			if w, _ := panel.Size(it, m, tm); w != m.Width {
				t.Fatalf("dpi %d %s: %d wide, the panel is %d", dpi, name, w, m.Width)
			}
		}
	}
}
