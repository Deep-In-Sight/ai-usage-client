package panel

import "github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"

// Item is what a helper draws for one menu item of the panel: a section's
// header (with its problem line, drawn under it) or one of its rows.
type Item struct {
	Kind string `json:"kind"` // "header" | "row" | "more"

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
