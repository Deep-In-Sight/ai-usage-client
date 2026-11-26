package panel

import (
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// Item is what a helper draws for one menu item of the panel: a section's
// header (with its problem line, drawn under it) or one of its rows.
type Item struct {
	Kind string `json:"kind"` // "header" | "row" | "more" | "action" | "info"

	// header
	Provider string `json:"provider"` // also on rows
	Name     string `json:"name"`
	Account  string `json:"account"`
	Updated  string `json:"updated"`
	Problem  string `json:"problem"`

	// row
	Label       string   `json:"label"`
	PercentText string   `json:"percent_text"`
	Detail      string   `json:"detail"`
	Level       string   `json:"level"`
	Percent     *float64 `json:"percent"`

	Stale bool `json:"stale"` // the section's numbers are old: drawn at StaleOpacity

	// text items, which only Windows draws itself ("action": newversion
	// and the actions, behind a check-mark column; "info": the server
	// line and the sign-in prompt)
	Text     string `json:"text"`
	Enabled  bool   `json:"enabled"`
	Checkbox bool   `json:"checkbox"`
	Checked  bool   `json:"checked"`
	Dim      bool   `json:"dim"` // an info line in the dim colour (the server line)
}

// Items are the panel's drawn items by menu item id: "h.<provider>" for
// each section's header (carrying its problem line), "r.<provider>.<n>"
// for each row ("more" for "More limits on the dashboard").
func Items(p trayproto.Panel) map[string]Item {
	out := map[string]Item{}
	for _, s := range p.Sections {
		out["h."+s.Provider] = Item{Kind: "header", Provider: s.Provider, Name: s.Name, Account: s.Account,
			Updated: s.Updated, Problem: s.Problem, Stale: s.Stale}
		for _, r := range s.Rows {
			kind := "row"
			if r.More {
				kind = "more"
			}
			out[r.ID] = Item{Kind: kind, Provider: s.Provider, Label: r.Label, PercentText: r.PercentText,
				Detail: r.Detail, Level: r.Level, Percent: r.Percent, Stale: s.Stale}
		}
	}
	return out
}

// AccessibleText is what a screen reader says for a drawn item (macOS
// gives it to VoiceOver as the item's label and makes it the menu item's
// title): its texts in reading order, a comma between them; a header's
// problem line after its time; what only the colour or the opacity shows,
// in words (review A3-M6): a row's warning or critical level after its
// percent, "not current" last for a section whose numbers are old.
func AccessibleText(it Item) string {
	var parts []string
	switch it.Kind {
	case "header":
		parts = []string{it.Name, it.Account, it.Updated, it.Problem}
	case "row":
		level := map[string]string{"warning": text.SaysWarning, "critical": text.SaysCritical}[it.Level]
		parts = []string{it.Label, it.PercentText, level, it.Detail}
	case "more":
		parts = []string{it.Label}
	default:
		parts = []string{it.Text}
	}
	if it.Stale && (it.Kind == "header" || it.Kind == "row") {
		parts = append(parts, text.SaysNotCurrent)
	}
	return strings.Join(slicesNonEmpty(parts), ", ")
}

func slicesNonEmpty(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
