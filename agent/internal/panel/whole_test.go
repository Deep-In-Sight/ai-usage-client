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

// wholeItems are the fixtures' items and the longest texts a real menu
// gets: the longest account with the PCs and a plan, a wrapped problem,
// the server line naming two fetching PCs, every action.
func wholeItems() map[string]panel.Item {
	out := map[string]panel.Item{
		"long account": {Kind: "header", Provider: "codex", Name: "Codex", Account: "firstname.lastname.…@… · 12 PCs · Enterprise", Updated: "Updated Mon 15:30"},
		"problem":      {Kind: "header", Provider: "codex", Name: "Codex", Problem: strings.Repeat("Sign-in expired: open Codex to renew it ", 3)},
		"two hosts":    {Kind: "info", Text: "Server connected · Claude from DESKTOP-ABCDEFG · Codex from DESKTOP-HIJKLMN", Dim: true},
		"prompt":       {Kind: "info", Text: "Sign in to Claude Code or Codex"},
		"checked":      {Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true},
		"long action":  {Kind: "action", Text: strings.Repeat("Remove AI Usage ", 4), Enabled: true},
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
// stays as a guard; a step it had to cut fails here.
func TestLayoutDrawsEveryTextWhole(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		m, tm := panel.MetricsAt(dpi), measurer{dpi}
		for name, it := range wholeItems() {
			w, h := panel.Size(it, m, tm)
			for _, extra := range []int{0, 43} {
				for _, sel := range []bool{false, true} {
					ops := panel.Layout(it, m, tm, w+extra, h, panel.DarkPalette, sel)
					if miss := panelcheck.Missing(it, ops, m, tm, w+extra, h); len(miss) > 0 {
						t.Fatalf("dpi %d %s (%dx%d): %s", dpi, name, w+extra, h, strings.Join(miss, "; "))
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
