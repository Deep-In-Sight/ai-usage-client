// package tray_test: the fixture package imports tray (pre-flight F3).
package tray_test

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

func pf(v float64) *float64 { return &v }

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// setP changes one provider's part of in's snapshot (the fixture's map is
// not shared with the caller's other inputs).
func setP(in *tray.MenuInputs, id provider.ID, f func(c *status.ProviderStatus)) {
	m := maps.Clone(in.Snap.Providers)
	c := m[id]
	c.Rows = slices.Clone(c.Rows)
	f(&c)
	m[id] = c
	in.Snap.Providers = m
}

// menuItem is BuildMenu's item id.
func menuItem(t *testing.T, items []trayproto.Item, id string) trayproto.Item {
	t.Helper()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %s", id)
	return trayproto.Item{}
}

// menuRowIDs are the visible "r." items of the menu, in order.
func menuRowIDs(items []trayproto.Item) []string {
	out := []string{}
	for _, it := range items {
		if it.Visible && strings.HasPrefix(it.ID, "r.") {
			out = append(out, it.ID)
		}
	}
	return out
}

func panelRowIDs(p trayproto.Panel) []string {
	out := []string{}
	for _, s := range p.Sections {
		for _, r := range s.Rows {
			out = append(out, r.ID)
		}
	}
	return out
}

func section(t *testing.T, p trayproto.Panel, prov string) trayproto.PanelSection {
	t.Helper()
	for _, s := range p.Sections {
		if s.Provider == prov {
			return s
		}
	}
	t.Fatalf("no section %s in %+v", prov, p.Sections)
	return trayproto.PanelSection{}
}

// at is a time on the fixture's Friday.
func at(h, m int) *time.Time {
	v := time.Date(2026, 11, 27, h, m, 0, 0, time.UTC)
	return &v
}

func limits(n int) []provider.Row {
	var rows []provider.Row
	for i := range n {
		rows = append(rows, provider.Row{Key: "codex:k" + string(rune('a'+i)), Label: "L" + string(rune('a'+i)),
			Percent: pf(float64(10 * i)), ResetsAt: at(21, 0), Level: usage.LevelNormal})
	}
	return rows
}

// The panel has the menu's rows, with the same ids, in every case
// (design 5.3: an expired sign-in keeps its last rows).
func TestPanelRowsMatchMenu(t *testing.T) {
	noFive := func(in *tray.MenuInputs) {
		setP(in, provider.Claude, func(c *status.ProviderStatus) { c.Rows = c.Rows[1:]; c.MainKey = "weekly_all" })
	}
	noFable := func(in *tray.MenuInputs) {
		setP(in, provider.Claude, func(c *status.ProviderStatus) { c.Rows = c.Rows[:2] })
	}
	codexRows := func(n int) func(in *tray.MenuInputs) {
		return func(in *tray.MenuInputs) {
			setP(in, provider.Codex, func(c *status.ProviderStatus) { c.Rows = limits(n); c.MainKey = "" })
		}
	}
	expired := func(in *tray.MenuInputs) {
		setP(in, provider.Claude, func(c *status.ProviderStatus) { c.State = "expired" })
	}
	for name, change := range map[string]func(*tray.MenuInputs){
		"combined": func(*tray.MenuInputs) {}, "no 5-hour": noFive, "no Fable": noFable,
		"no 5-hour, no Fable": func(in *tray.MenuInputs) { noFable(in); noFive(in) },
		"codex 3":             codexRows(3), "codex 4": codexRows(4), "codex 6": codexRows(6), "claude expired": expired,
	} {
		t.Run(name, func(t *testing.T) {
			in := panelfixture.Combined()
			change(&in)
			p := tray.BuildPanel(in)
			eq(t, panelRowIDs(p), menuRowIDs(tray.BuildMenu(in)))
		})
	}
	in := panelfixture.Combined()
	codexRows(6)(&in)
	rows := section(t, tray.BuildPanel(in), "codex").Rows
	eq(t, len(rows), 5)
	eq(t, rows[3].Label, "Ld")
	eq(t, rows[4], trayproto.PanelRow{ID: "r.codex.4", Label: "More limits on the dashboard", More: true, Level: "none"})
	in = panelfixture.Combined()
	expired(&in)
	eq(t, len(section(t, tray.BuildPanel(in), "claude").Rows), 3)
}

func TestPanelTexts(t *testing.T) {
	in := panelfixture.Combined()
	p := tray.BuildPanel(in)
	eq(t, p.Serial, uint64(0)) // the tray loop numbers the panels (PR-6)
	eq(t, len(p.Sections), 2)
	c := p.Sections[0]
	eq(t, []string{c.Provider, c.Name, c.Account, c.Updated, c.Problem}, []string{"claude", "Claude", "team-a@… · 3 PCs", "Updated 18:52", ""})
	eq(t, c.Stale, false)
	eq(t, c.Rows[0], trayproto.PanelRow{ID: "r.claude.0", Label: "5-hour", Percent: pf(42), PercentText: "42%",
		Detail: "Resets Fri 21:20 · in 2h 20m", Level: "normal"})
	eq(t, c.Rows[1].Detail, "Resets Sun 01:00 · in 1d 6h")
	eq(t, c.Rows[2].Label, "Fable")
	x := p.Sections[1]
	eq(t, []string{x.Provider, x.Name, x.Account, x.Updated}, []string{"codex", "Codex", "bob@… · 1 PC · Plus", "Updated 18:52"})
	eq(t, x.Rows[0], trayproto.PanelRow{ID: "r.codex.0", Label: "5-hour", Percent: pf(100), PercentText: "100%",
		Detail: "Blocked · resets Fri 19:35 · in 35m", Level: "critical"})
	eq(t, x.Rows[1].Detail, "Resets Sat 22:00 · in 1d 3h")
	// the header's parts are the menu's header
	items := tray.BuildMenu(in)
	for _, s := range p.Sections {
		eq(t, s.Name+" · "+s.Account, menuItem(t, items, "h."+s.Provider).Text)
	}

	detail := func(f func(r *provider.Row)) string {
		in := panelfixture.Combined()
		setP(&in, provider.Claude, func(c *status.ProviderStatus) { f(&c.Rows[0]) })
		return section(t, tray.BuildPanel(in), "claude").Rows[0].Detail
	}
	eq(t, detail(func(r *provider.Row) { r.ResetsAt = at(18, 0) }), "Reset due Fri 18:00")
	eq(t, detail(func(r *provider.Row) { r.ResetsAt = nil }), "") // the row keeps its height (PR-7)
	eq(t, detail(func(r *provider.Row) { r.ResetsAt = &time.Time{} }), "")
	eq(t, detail(func(r *provider.Row) { r.Blocked = true }), "Blocked · resets Fri 21:20 · in 2h 20m")
	eq(t, detail(func(r *provider.Row) { r.Blocked, r.ResetsAt = true, at(18, 0) }), "Blocked · reset due Fri 18:00")
	eq(t, detail(func(r *provider.Row) { r.Blocked, r.ResetsAt = true, nil }), "Blocked") // F6
	// times in now's location, as the menu shows them
	in.Now = in.Now.In(time.FixedZone("UTC+9", 9*3600))
	p = tray.BuildPanel(in)
	eq(t, p.Sections[0].Rows[0].Detail, "Resets Sat 06:20 · in 2h 20m")
	eq(t, p.Sections[0].Updated, "Updated 03:52")
}

// Each problem line is the menu's; the Codex-only server line stays in
// the Codex section; an expired sign-in keeps its rows.
func TestPanelProblems(t *testing.T) {
	for _, c := range []struct {
		id       provider.ID
		state    string
		parse    string
		fallback bool
		want     string
	}{
		{provider.Claude, "expired", "", false, "Sign-in expired: open Claude Code to renew it"},
		{provider.Codex, "expired", "", false, "Codex sign-in expired: run Codex once to renew it"},
		{provider.Codex, "unsupported_store", "", false, "Codex sign-in is stored in the keychain: not supported"},
		{provider.Codex, "api_key", "", false, "Codex API-key sign-in has no plan limits"},
		{provider.Codex, "unsupported_base_url", "", false, "Codex server address in config.toml is not supported"},
		{provider.Claude, "ok", "no_weekly", false, "Usage reply could not be read"},
		{provider.Codex, "ok", "no_weekly", false, "Usage reply could not be read"},
		{provider.Codex, "ok", "", true, "Server does not support Codex yet"},
	} {
		in := panelfixture.Combined()
		in.Snap.SchemaFallback = c.fallback
		setP(&in, c.id, func(p *status.ProviderStatus) { p.State, p.ParseProblem = c.state, c.parse })
		p := tray.BuildPanel(in)
		items := tray.BuildMenu(in)
		for _, s := range p.Sections {
			menu := menuItem(t, items, "p."+s.Provider)
			want := ""
			if menu.Visible {
				want = menu.Text
			}
			eq(t, s.Problem, want)
		}
		eq(t, section(t, p, string(c.id)).Problem, c.want)
		if c.fallback {
			eq(t, section(t, p, "claude").Problem, "") // Claude goes on with schema 1
		}
		if c.state == "expired" {
			eq(t, len(section(t, p, string(c.id)).Rows), len(in.Snap.Providers[c.id].Rows))
		}
	}
}

func TestPanelPromptAndNewVersion(t *testing.T) {
	in := panelfixture.Combined()
	for _, id := range []provider.ID{provider.Claude, provider.Codex} {
		setP(&in, id, func(c *status.ProviderStatus) { *c = status.ProviderStatus{State: "not_signed_in"} })
	}
	p := tray.BuildPanel(in)
	eq(t, p.Sections, []trayproto.PanelSection{})
	eq(t, p.Prompt, "Sign in to Claude Code or Codex")
	eq(t, p.Prompt, menuItem(t, tray.BuildMenu(in), "p.claude").Text)
	b, err := json.Marshal(p)
	if err != nil || !strings.Contains(string(b), `"sections":[]`) {
		t.Fatalf("%v %s", err, b)
	}
	eq(t, tray.BuildPanel(panelfixture.Combined()).Prompt, "")
	// the new-version line is finished text (pre-flight F2)
	in = panelfixture.Combined()
	eq(t, tray.BuildPanel(in).NewVersion, "")
	in.Snap.ClientLatestVersion = "0.9.2"
	eq(t, tray.BuildPanel(in).NewVersion, "New version available: 0.9.2")
	eq(t, tray.BuildPanel(in).NewVersion, menuItem(t, tray.BuildMenu(in), "newversion").Text)
	in.Version = "0.9.2"
	eq(t, tray.BuildPanel(in).NewVersion, "")
	// before the first status: nothing to show but the server line
	in.Snap = status.Snapshot{}
	p = tray.BuildPanel(in)
	eq(t, p.Sections, []trayproto.PanelSection{})
	eq(t, p.Prompt, "")
	eq(t, p.Server, "Connecting to the server…")
}

func TestPanelStale(t *testing.T) {
	p := tray.BuildPanel(panelfixture.States())
	c := section(t, p, "claude")
	eq(t, c.Stale, true)
	eq(t, c.Updated, "Updated 18:30") // no ⚠ in the panel
	eq(t, section(t, p, "codex").Stale, false)
	eq(t, p.Top[0].Provider, "claude")
	eq(t, p.Top[0].Text, "42% ⚠")
	eq(t, p.Top[0].Stale, true)
	eq(t, p.Top[1].Stale, false)
}

func TestPanelServerHasNoUpdated(t *testing.T) {
	in := panelfixture.Combined()
	eq(t, tray.BuildPanel(in).Server, "Server connected · this PC fetches Claude, Codex")
	eq(t, menuItem(t, tray.BuildMenu(in), "server").Text, "Server connected · Updated 18:52 · this PC fetches Claude, Codex")
	for _, id := range []provider.ID{provider.Claude, provider.Codex} {
		setP(&in, id, func(c *status.ProviderStatus) { c.Role, c.PollerHostname = "standby", "TEST-PC-03" })
	}
	eq(t, tray.BuildPanel(in).Server, "Server connected · Claude from TEST-PC-03 · Codex from TEST-PC-03")
	eq(t, menuItem(t, tray.BuildMenu(in), "server").Text, "Server connected · Updated 18:52 · Claude from TEST-PC-03 · Codex from TEST-PC-03")
	in = panelfixture.Combined()
	for _, id := range []provider.ID{provider.Claude, provider.Codex} {
		setP(&in, id, func(c *status.ProviderStatus) { c.LatestFetchedAt = time.Time{} })
	}
	eq(t, tray.BuildPanel(in).Server, "Server connected · no usage yet · this PC fetches Claude, Codex")
	in = panelfixture.Combined()
	in.Snap.Mode = "standalone"
	eq(t, tray.BuildPanel(in).Server, "Server not reachable · this PC fetches every 30 min")
	eq(t, menuItem(t, tray.BuildMenu(in), "server").Text, "Server not reachable · this PC fetches every 30 min · Updated 18:52")
}

// A row's level is the icon's rule: the stored level, from the percent
// when none was stored (pre-flight F5: the providers store the reply's
// severity as the level).
func TestPanelLevels(t *testing.T) {
	for _, c := range []struct {
		pct   *float64
		level usage.Level
		want  string
	}{
		{pf(74.9), "", "normal"}, {pf(75), "", "warning"}, {pf(89.9), "", "warning"}, {pf(90), "", "critical"},
		{pf(10), usage.LevelWarning, "warning"}, {nil, "", "none"}, {nil, usage.LevelNormal, "none"},
	} {
		in := panelfixture.Combined()
		setP(&in, provider.Claude, func(p *status.ProviderStatus) { p.Rows[0].Percent, p.Rows[0].Level = c.pct, c.level })
		p := tray.BuildPanel(in)
		eq(t, p.Sections[0].Rows[0].Level, c.want)
		// the top bar shows that row: the same level as the icon
		eq(t, p.Top[0].Level, c.want)
		eq(t, p.Top[0].Level, tray.BuildIcon(provider.Claude, in).Icon.Level)
	}
}

func TestPanelPercentIsRounded(t *testing.T) {
	for _, c := range []struct {
		pct  *float64
		want *float64
		text string
	}{
		{pf(4.5), pf(5), "5%"}, {pf(99.6), pf(100), "100%"}, {pf(130), pf(130), "130%"}, {pf(0.2), pf(0), "0%"}, {nil, nil, "–%"},
	} {
		in := panelfixture.Combined()
		setP(&in, provider.Claude, func(p *status.ProviderStatus) { p.Rows[0].Percent = c.pct })
		r := tray.BuildPanel(in).Sections[0].Rows[0]
		eq(t, r.Percent, c.want)
		eq(t, r.PercentText, c.text)
	}
}

func TestPanelActionsMatchMenu(t *testing.T) {
	armed := panelfixture.Combined()
	armed.RemoveArmed, armed.Snap.FreshPending = true, true
	bare := panelfixture.Combined()
	bare.HasToggle, bare.AutostartOn, bare.DashboardURL = false, false, ""
	for name, in := range map[string]tray.MenuInputs{"combined": panelfixture.Combined(), "armed": armed, "bare": bare} {
		t.Run(name, func(t *testing.T) {
			p := tray.BuildPanel(in)
			items := tray.BuildMenu(in)
			var ids []string
			for _, a := range p.Actions {
				ids = append(ids, a.ID)
				m := menuItem(t, items, a.ID)
				eq(t, a, trayproto.PanelAction{ID: m.ID, Text: m.Text, Enabled: m.Enabled, Visible: m.Visible, Checkbox: m.Checkbox, Checked: m.Checked})
			}
			eq(t, ids, []string{"dashboard", "refresh", "autostart", "remove", "quit"})
		})
	}
	a := tray.BuildPanel(armed).Actions
	eq(t, a[1], trayproto.PanelAction{ID: "refresh", Text: "Refreshing…", Visible: true})
	eq(t, a[3].Text, "Click again to remove AI Usage")
	b := tray.BuildPanel(bare).Actions
	eq(t, b[0].Enabled, false)
	eq(t, b[2].Visible, false)
}

func TestPanelTop(t *testing.T) {
	top := func(in tray.MenuInputs) []trayproto.PanelTop { return tray.BuildPanel(in).Top }
	icon := func(p provider.ID, in tray.MenuInputs) trayproto.PanelTop {
		v := tray.BuildIcon(p, in)
		return trayproto.PanelTop{Provider: v.Icon.Provider, Text: v.Title, Level: v.Icon.Level, Percent: v.Icon.Percent, Stale: v.Icon.Stale}
	}
	in := panelfixture.Combined()
	eq(t, top(in), []trayproto.PanelTop{icon(provider.Claude, in), icon(provider.Codex, in)})
	eq(t, top(in)[0].Text, "42%")
	eq(t, top(in)[1], trayproto.PanelTop{Provider: "codex", Text: "100%", Level: "critical", Percent: pf(100)})
	setP(&in, provider.Codex, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	eq(t, top(in), []trayproto.PanelTop{icon(provider.Claude, in)})
	// a problem without a sign-in has a section but no number
	setP(&in, provider.Codex, func(c *status.ProviderStatus) { c.State = "api_key" })
	eq(t, len(top(in)), 1)
	in = panelfixture.Combined()
	setP(&in, provider.Claude, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	eq(t, top(in), []trayproto.PanelTop{icon(provider.Codex, in)})
	setP(&in, provider.Codex, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	eq(t, top(in), []trayproto.PanelTop{{Provider: "", Text: "–%", Level: "none"}})
	in.Snap = status.Snapshot{}
	eq(t, top(in), []trayproto.PanelTop{{Provider: "", Text: "–%", Level: "none"}})
}

// The JSON names are design 6.4's GetView names, fixed by a golden file
// (Global Constraint 4: they never change once the extension reads them).
func TestPanelJSONNames(t *testing.T) {
	got, err := json.Marshal(tray.BuildPanel(panelfixture.Combined()))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/panel-combined.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("panel JSON changed:\n got %s\nwant %s", got, want)
	}
	var v map[string]any
	if err := json.Unmarshal(want, &v); err != nil {
		t.Fatal(err)
	}
	names := func(m any) []string { return slices.Sorted(maps.Keys(m.(map[string]any))) }
	eq(t, names(v), []string{"actions", "new_version", "prompt", "sections", "serial", "server", "top"})
	sec := v["sections"].([]any)[0]
	eq(t, names(sec), []string{"account", "name", "problem", "provider", "rows", "stale", "updated"})
	eq(t, names(sec.(map[string]any)["rows"].([]any)[0]), []string{"detail", "id", "label", "level", "more", "percent", "percent_text"})
	eq(t, names(v["actions"].([]any)[0]), []string{"checkbox", "checked", "enabled", "id", "text", "visible"})
	eq(t, names(v["top"].([]any)[0]), []string{"level", "percent", "provider", "stale", "text"})
}
