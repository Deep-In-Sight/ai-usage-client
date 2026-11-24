package tray

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

var loc = time.FixedZone("UTC+9", 9*3600)
var now = time.Date(2026, 11, 20, 19, 0, 0, 0, loc) // Fri 19:00

func pf(v float64) *float64 { return &v }

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

const fs = " " // figure space: pads a percent to the width of "100%"

func full() status.Snapshot {
	resets := now.Add(30 * time.Hour).UTC()
	return status.Snapshot{Mode: "coordinated", Providers: map[provider.ID]status.ProviderStatus{provider.Claude: {
		Role: "standby", State: "ok", AccountEmail: "team-a@example.com", AccountKey: "acc-0001",
		PeersPCs: 3, PollerHostname: "TEST-PC-03", LatestFetchedAt: now.Add(-2 * time.Minute).UTC(), MainKey: "weekly_all",
		Rows: []provider.Row{
			{Key: "weekly_all", Label: "Weekly", Percent: pf(71), ResetsAt: &resets, Level: usage.LevelNormal, WindowMins: 10080},
			{Key: "weekly_scoped:Fable", Label: "Fable", Percent: pf(76), ResetsAt: &resets, Level: usage.LevelWarning, WindowMins: 10080},
		}}}}
}

// set changes one provider's part of in's snapshot.
func setP(in *MenuInputs, id provider.ID, f func(c *status.ProviderStatus)) {
	c := in.Snap.Providers[id]
	f(&c)
	m := map[provider.ID]status.ProviderStatus{}
	for k, v := range in.Snap.Providers {
		m[k] = v
	}
	m[id] = c
	in.Snap.Providers = m
}

func set(in *MenuInputs, f func(c *status.ProviderStatus)) { setP(in, provider.Claude, f) }

// setWeekly sets Claude's weekly row as the engine computes it from a
// percentage and the reply's severity ("" reads as normal).
func setWeekly(in *MenuInputs, pct *float64, sev string) {
	if sev == "" {
		sev = "normal"
	}
	set(in, func(c *status.ProviderStatus) {
		rows := append([]provider.Row(nil), c.Rows...)
		for i := range rows {
			if rows[i].Key == "weekly_all" {
				rows[i].Percent, rows[i].Level = pct, usage.LevelOf(usage.Limit{Percent: pct, Severity: sev})
			}
		}
		c.Rows = rows
	})
}

func base() MenuInputs {
	return MenuInputs{Snap: full(), Now: now, Version: "1.0.0", HasToggle: true, AutostartOn: true, DashboardURL: "http://ai-usage.local"}
}

func item(t *testing.T, items []trayproto.Item, id string) trayproto.Item {
	t.Helper()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %s", id)
	return trayproto.Item{}
}

// textOf is an item's text when it is shown ("" when hidden).
func textOf(items []trayproto.Item, id string) string {
	for _, it := range items {
		if it.ID == id && it.Visible {
			return it.Text
		}
	}
	return ""
}

func visible(items []trayproto.Item, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return it.Visible
		}
	}
	return false
}

// shown is the menu as a user reads it: the visible items' texts, with
// "-" where a separator shows (the helper puts one after each section
// shown and after the server line).
func shown(items []trayproto.Item) []string {
	var out []string
	sections := map[string]bool{}
	for _, it := range items {
		if s := trayproto.Section(it.ID); s != "" && it.Visible {
			sections[s] = true
		}
	}
	for _, it := range items {
		if it.Visible {
			out = append(out, it.Text)
		}
		if s := trayproto.Section(it.ID); s != "" && it.ID == "p."+s && sections[s] || it.ID == "server" {
			out = append(out, "-")
		}
	}
	return out
}

func TestBar(t *testing.T) {
	for _, c := range []struct {
		p    *float64
		want string
	}{
		{nil, "░░░░░░░░░░"},
		{pf(0), "░░░░░░░░░░"},
		{pf(4), "░░░░░░░░░░"},
		{pf(5), "▌░░░░░░░░░"},
		{pf(10), "█░░░░░░░░░"},
		{pf(65), "██████▌░░░"},
		{pf(99.6), "█████████▌"},
		{pf(100), "██████████"},
		{pf(130), "██████████"},
		{pf(-3), "░░░░░░░░░░"},
	} {
		got := Bar(c.p)
		if got != c.want {
			t.Fatalf("Bar(%v) = %q, want %q", c.p, got, c.want)
		}
		if n := len([]rune(got)); n != 10 {
			t.Fatalf("Bar(%v) has %d cells", c.p, n)
		}
	}
}

func TestRowText(t *testing.T) {
	n := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	resets := time.Date(2026, 11, 29, 16, 0, 0, 0, time.UTC)
	r := provider.Row{Key: "weekly_all", Label: "Weekly", Percent: pf(65), ResetsAt: &resets}
	eq(t, RowText(r, n), "██████▌░░░  "+fs+"65%  Weekly · Sun 16:00 (1d 6h)")
	// every percent takes the width of "100%" (figure spaces are as wide
	// as a digit), so the labels line up in a proportional font
	r.Percent = pf(5)
	eq(t, RowText(r, n), "▌░░░░░░░░░  "+fs+fs+"5%  Weekly · Sun 16:00 (1d 6h)")
	r.Percent = pf(100)
	eq(t, RowText(r, n), "██████████  100%  Weekly · Sun 16:00 (1d 6h)")
	r.Percent = nil
	eq(t, RowText(r, n), "░░░░░░░░░░  "+fs+fs+"–%  Weekly · Sun 16:00 (1d 6h)")
	// a blocked row says so after its name; no reset time: no reset part
	r = provider.Row{Key: "codex:5h", Label: "5-hour", Percent: pf(100), Blocked: true}
	eq(t, RowText(r, n), "██████████  100%  5-hour blocked")
	// within the day: hours and minutes; past due
	soon := n.Add(35 * time.Minute)
	r.ResetsAt = &soon
	eq(t, RowText(r, n), "██████████  100%  5-hour blocked · Sat 10:35 (35m)")
	past := n.Add(-time.Minute)
	r.ResetsAt = &past
	eq(t, RowText(r, n), "██████████  100%  5-hour blocked · Sat 09:59 (due)")
}

func TestMenuOneProvider(t *testing.T) {
	items := BuildMenu(base())
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	eq(t, ids, trayproto.ItemIDs)
	eq(t, shown(items), []string{
		"Claude · team-a@… · 3 PCs",
		"███████░░░  " + fs + "71%  Weekly · Sun 01:00 (1d 6h)",
		"███████▌░░  " + fs + "76%  Fable · Sun 01:00 (1d 6h)",
		"-",
		"Server connected · Updated 18:58 · fetching: TEST-PC-03",
		"-",
		"Open dashboard", "Refresh now", "Start at login", "Remove AI Usage", "Quit",
	})
	// the Codex section is not there at all
	for _, id := range trayproto.ItemIDs {
		if trayproto.Section(id) == "codex" && visible(items, id) {
			t.Fatalf("%s shown", id)
		}
	}
	h := item(t, items, "h.claude")
	eq(t, h.Image, "claude")
	eq(t, h.Enabled, false)
	// usage rows are clickable (Windows greys out disabled items)
	eq(t, item(t, items, "r.claude.0").Enabled, true)
	eq(t, item(t, items, "server").Enabled, false)
	for _, id := range []string{"dashboard", "refresh", "autostart", "remove", "quit"} {
		eq(t, item(t, items, id).Enabled, true)
	}
	a := item(t, items, "autostart")
	eq(t, a.Checkbox && a.Checked, true)
	eq(t, visible(items, "newversion"), false)
	eq(t, visible(items, "p.claude"), false)
}

// With a 5-hour value the row comes first (shorter windows first, D5).
func TestMenuWithFiveHour(t *testing.T) {
	in := base()
	soon := now.Add(2*time.Hour + 20*time.Minute).UTC()
	set(&in, func(c *status.ProviderStatus) {
		c.Rows = append([]provider.Row{{Key: "five_hour", Label: "5-hour", Percent: pf(42), ResetsAt: &soon, Level: usage.LevelNormal, WindowMins: 300}}, c.Rows...)
	})
	items := BuildMenu(in)
	eq(t, textOf(items, "r.claude.0"), "████░░░░░░  "+fs+"42%  5-hour · Fri 21:20 (2h 20m)")
	eq(t, textOf(items, "r.claude.1"), "███████░░░  "+fs+"71%  Weekly · Sun 01:00 (1d 6h)")
	eq(t, textOf(items, "r.claude.2"), "███████▌░░  "+fs+"76%  Fable · Sun 01:00 (1d 6h)")
	eq(t, visible(items, "r.claude.3"), false)
}

// Two providers (a second, fake one): both sections in a fixed order,
// each followed by a separator, then the server line.
func TestMenuTwoProviders(t *testing.T) {
	in := base()
	r5 := now.Add(35 * time.Minute).UTC()
	rw := now.Add(27 * time.Hour).UTC()
	setP(&in, provider.Codex, func(c *status.ProviderStatus) {
		*c = status.ProviderStatus{State: "ok", AccountEmail: "bob@example.com", AccountKey: "codex:a:b", Plan: "plus", PeersPCs: 1,
			Role: "poller", LatestFetchedAt: now.Add(-time.Minute).UTC(), MainKey: "codex:weekly", Rows: []provider.Row{
				{Key: "codex:5h", Label: "5-hour", Percent: pf(100), Blocked: true, ResetsAt: &r5, Level: usage.LevelCritical, WindowMins: 300},
				{Key: "codex:weekly", Label: "Weekly", Percent: pf(38), ResetsAt: &rw, Level: usage.LevelNormal, WindowMins: 10080},
			}}
	})
	eq(t, shown(BuildMenu(in)), []string{
		"Claude · team-a@… · 3 PCs",
		"███████░░░  " + fs + "71%  Weekly · Sun 01:00 (1d 6h)",
		"███████▌░░  " + fs + "76%  Fable · Sun 01:00 (1d 6h)",
		"-",
		"Codex · bob@… · 1 PC · Plus",
		"██████████  100%  5-hour blocked · Fri 19:35 (35m)",
		"███▌░░░░░░  " + fs + "38%  Weekly · Sat 22:00 (1d 3h)",
		"-",
		"Server connected · Updated 18:58 · this PC fetches Codex · Claude from TEST-PC-03",
		"-",
		"Open dashboard", "Refresh now", "Start at login", "Remove AI Usage", "Quit",
	})
	eq(t, item(t, BuildMenu(in), "h.codex").Image, "codex")
}

// Codex has room for four rows; more limits end with a pointer to the
// dashboard; Claude has five.
func TestMenuRowLimits(t *testing.T) {
	in := base()
	var rows []provider.Row
	for i := range 7 {
		rows = append(rows, provider.Row{Key: "k" + string(rune('a'+i)), Label: "L" + string(rune('a'+i)), Percent: pf(float64(10 * i))})
	}
	setP(&in, provider.Codex, func(c *status.ProviderStatus) {
		*c = status.ProviderStatus{State: "ok", Rows: rows, AccountKey: "codex:a:b"}
	})
	set(&in, func(c *status.ProviderStatus) { c.Rows = rows })
	items := BuildMenu(in)
	for i := range 5 {
		if !strings.HasSuffix(textOf(items, "r.claude."+string(rune('0'+i))), "L"+string(rune('a'+i))) {
			t.Fatalf("claude row %d: %q", i, textOf(items, "r.claude."+string(rune('0'+i))))
		}
	}
	for i := range 4 {
		if !strings.HasSuffix(textOf(items, "r.codex."+string(rune('0'+i))), "L"+string(rune('a'+i))) {
			t.Fatalf("codex row %d: %q", i, textOf(items, "r.codex."+string(rune('0'+i))))
		}
	}
	eq(t, textOf(items, "r.codex.4"), "More limits on the dashboard")
	eq(t, RowURL(in, "r.codex.4"), "http://ai-usage.local/accounts/codex:a:b")
}

func TestRowsClickOpenAccountPage(t *testing.T) {
	in := base()
	setP(&in, provider.Codex, func(c *status.ProviderStatus) {
		*c = status.ProviderStatus{State: "ok", AccountKey: "codex:a b/c", Rows: []provider.Row{{Key: "codex:weekly", Label: "Weekly", Percent: pf(3)}}}
	})
	eq(t, RowURL(in, "r.claude.1"), "http://ai-usage.local/accounts/acc-0001")
	eq(t, RowURL(in, "r.codex.0"), "http://ai-usage.local/accounts/codex:a%20b%2Fc")
	eq(t, AccountURL("http://ai-usage.local/", "codex:a:b"), "http://ai-usage.local/accounts/codex:a:b")
	eq(t, RowURL(in, "r.codex.3"), "") // no such row
	eq(t, RowURL(in, "h.claude"), "")
	in.DashboardURL = ""
	eq(t, RowURL(in, "r.claude.0"), "")
	set(&in, func(c *status.ProviderStatus) { c.AccountKey = "" })
	in.DashboardURL = "http://ai-usage.local"
	eq(t, RowURL(in, "r.claude.0"), "http://ai-usage.local") // no account known: the dashboard
}

func TestBlockedRowDrivesIconAndTooltip(t *testing.T) {
	in := base()
	soon := now.Add(80 * time.Minute).UTC()
	set(&in, func(c *status.ProviderStatus) {
		c.Rows = append([]provider.Row{{Key: "five_hour", Label: "5-hour", Percent: pf(100), Blocked: true, ResetsAt: &soon, Level: usage.LevelCritical}}, c.Rows...)
	})
	v := BuildIcon(provider.Claude, in)
	eq(t, *v.Icon.Percent, 100.0)
	eq(t, v.Icon.Level, string(usage.LevelCritical))
	eq(t, v.Title, "100%")
	eq(t, v.Tooltip, "AI Usage · Claude\n5-hour 100% blocked · Fri 20:20 (1h 20m)\nUpdated 18:58")
	// nothing at 100 %: the main row, as v0.8 showed it
	v = BuildIcon(provider.Claude, base())
	eq(t, *v.Icon.Percent, 71.0)
	eq(t, v.Icon.Provider, "claude")
	eq(t, v.Tooltip, "AI Usage · Claude\nWeekly 71% · Sun 01:00 (1d 6h)\nUpdated 18:58")
}

func TestSchemaFallbackLine(t *testing.T) {
	in := base()
	in.Snap.SchemaFallback = true
	setP(&in, provider.Codex, func(c *status.ProviderStatus) {
		*c = status.ProviderStatus{State: "ok", Rows: []provider.Row{{Key: "codex:weekly", Label: "Weekly", Percent: pf(3)}}}
	})
	items := BuildMenu(in)
	eq(t, textOf(items, "p.codex"), "Server does not support Codex yet")
	eq(t, visible(items, "p.claude"), false) // Claude goes on with schema 1
}

func TestProblemLines(t *testing.T) {
	for _, c := range []struct {
		id    provider.ID
		state string
		parse string
		want  string
	}{
		{provider.Claude, "expired", "", "Sign-in expired: open Claude Code to renew it"},
		{provider.Codex, "expired", "", "Codex sign-in expired: run Codex once to renew it"},
		{provider.Codex, "unsupported_store", "", "Codex sign-in is stored in the keychain: not supported"},
		{provider.Codex, "api_key", "", "Codex API-key sign-in has no plan limits"},
		{provider.Codex, "unsupported_base_url", "", "Codex server address in config.toml is not supported"},
		{provider.Claude, "ok", "no_weekly", "Usage reply could not be read"},
		{provider.Codex, "ok", "no_weekly", "Usage reply could not be read"},
	} {
		in := base()
		setP(&in, c.id, func(p *status.ProviderStatus) { p.State, p.ParseProblem = c.state, c.parse })
		items := BuildMenu(in)
		eq(t, textOf(items, "p."+string(c.id)), c.want)
		eq(t, item(t, items, "p."+string(c.id)).Enabled, false)
	}
}

// Expired: the account is still there, so are its numbers.
func TestViewExpiredKeepsNumbers(t *testing.T) {
	in := base()
	set(&in, func(c *status.ProviderStatus) { c.State = "expired" })
	items := BuildMenu(in)
	eq(t, BuildIcon(provider.Claude, in).Title, "71%")
	eq(t, visible(items, "r.claude.1"), true)
	eq(t, BuildIcon(provider.Claude, in).Tooltip, "AI Usage · Claude\nWeekly 71% · Sun 01:00 (1d 6h)\nSign-in expired: open Claude Code to renew it")
}

// Nobody signed in: only the sign-in line in Claude's place, the neutral
// icon with "–%".
func TestViewNotSignedIn(t *testing.T) {
	in := base()
	set(&in, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	items := BuildMenu(in)
	eq(t, shown(items)[:3], []string{"Sign in to Claude Code", "-", "Server connected"})
	v := BuildIcon("", in)
	eq(t, v.Title, "–%")
	eq(t, v.Icon.Provider, "")
	eq(t, v.Icon.Percent == nil, true)
	eq(t, v.Icon.Level, string(usage.LevelNone))
	eq(t, v.Tooltip, "AI Usage\nSign in to Claude Code")
	// a build that reads Codex too names both
	setP(&in, provider.Codex, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	eq(t, textOf(BuildMenu(in), "p.claude"), "Sign in to Claude Code or Codex")
	// before the first status nothing is said about signing in
	in.Snap = status.Snapshot{}
	eq(t, shown(BuildMenu(in))[0], "Connecting to the server…")
}

func TestViewStale(t *testing.T) {
	in := base()
	set(&in, func(c *status.ProviderStatus) { c.LatestFetchedAt = now.Add(-16 * time.Minute) })
	v := BuildIcon(provider.Claude, in)
	eq(t, v.Title, "71% ⚠")
	eq(t, v.Icon.Stale, true)
	eq(t, textOf(BuildMenu(in), "server"), "Server connected · Updated 18:44 ⚠ · fetching: TEST-PC-03")
	set(&in, func(c *status.ProviderStatus) { c.LatestFetchedAt = now.Add(-14 * time.Minute) })
	v = BuildIcon(provider.Claude, in)
	eq(t, v.Title, "71%")
	eq(t, v.Icon.Stale, false)
}

func TestViewWarningFlag(t *testing.T) {
	in := base()
	setWeekly(&in, pf(40), "warning")
	eq(t, BuildIcon(provider.Claude, in).Icon.Level, string(usage.LevelWarning))
	setWeekly(&in, pf(40), "") // an old status file without severity reads as normal
	eq(t, BuildIcon(provider.Claude, in).Icon.Level, string(usage.LevelNormal))
	setWeekly(&in, pf(92), "normal")
	eq(t, BuildIcon(provider.Claude, in).Icon.Level, string(usage.LevelCritical))
	set(&in, func(c *status.ProviderStatus) { c.Rows[0].Level = "" }) // no level recorded: from the percent
	eq(t, BuildIcon(provider.Claude, in).Icon.Level, string(usage.LevelCritical))
}

func TestViewStandaloneAndStarting(t *testing.T) {
	in := base()
	in.Snap.Mode = "standalone"
	eq(t, textOf(BuildMenu(in), "server"), "Server not reachable · this PC fetches every 30 min · Updated 18:58")
	set(&in, func(c *status.ProviderStatus) { c.LatestFetchedAt = time.Time{} })
	eq(t, textOf(BuildMenu(in), "server"), "Server not reachable · this PC fetches every 30 min")
	in.Snap.Mode = "starting"
	eq(t, textOf(BuildMenu(in), "server"), "Connecting to the server…")
	in.Snap.Mode = ""
	eq(t, textOf(BuildMenu(in), "server"), "Connecting to the server…")
	in.Snap.Mode = "coordinated"
	eq(t, textOf(BuildMenu(in), "server"), "Server connected · no usage yet · fetching: TEST-PC-03")
}

func TestViewPollerSelf(t *testing.T) {
	in := base()
	set(&in, func(c *status.ProviderStatus) { c.Role = "poller" })
	eq(t, textOf(BuildMenu(in), "server"), "Server connected · Updated 18:58 · this PC fetches Claude")
	set(&in, func(c *status.ProviderStatus) { c.Role, c.PollerHostname = "standby", "" })
	eq(t, textOf(BuildMenu(in), "server"), "Server connected · Updated 18:58")
}

func TestViewHeader(t *testing.T) {
	in := base()
	set(&in, func(c *status.ProviderStatus) { c.PeersPCs = 1 })
	eq(t, textOf(BuildMenu(in), "h.claude"), "Claude · team-a@… · 1 PC")
	set(&in, func(c *status.ProviderStatus) { c.PeersPCs = 0 })
	eq(t, textOf(BuildMenu(in), "h.claude"), "Claude · team-a@…")
	set(&in, func(c *status.ProviderStatus) { c.AccountEmail = "" })
	eq(t, textOf(BuildMenu(in), "h.claude"), "Claude")
}

func TestViewNewVersion(t *testing.T) {
	in := base()
	in.Snap.ClientLatestVersion = "1.1.0"
	items := BuildMenu(in)
	eq(t, textOf(items, "newversion"), "New version available: 1.1.0")
	eq(t, item(t, items, "newversion").Enabled, true)
	in.Version = "0.0.0-dev"
	eq(t, visible(BuildMenu(in), "newversion"), false)
	in.Version = "1.1.0"
	eq(t, visible(BuildMenu(in), "newversion"), false)
}

func TestViewRemoveArmedAndRefreshing(t *testing.T) {
	in := base()
	in.RemoveArmed = true
	in.Snap.FreshPending = true
	items := BuildMenu(in)
	eq(t, textOf(items, "remove"), "Click again to remove AI Usage")
	eq(t, textOf(items, "refresh"), "Refreshing…")
	eq(t, item(t, items, "refresh").Enabled, false)
	in.HasToggle, in.DashboardURL = false, ""
	items = BuildMenu(in)
	eq(t, visible(items, "autostart"), false)
	eq(t, item(t, items, "dashboard").Enabled, false)
}

func TestViewUsesNowLocation(t *testing.T) {
	in := base()
	in.Now = now.UTC()
	eq(t, textOf(BuildMenu(in), "server"), "Server connected · Updated 09:58 · fetching: TEST-PC-03")
}

func TestTooltipLimit(t *testing.T) {
	in := base()
	long := strings.Repeat("x", 300)
	set(&in, func(c *status.ProviderStatus) {
		c.AccountEmail, c.PollerHostname, c.State = long+"@example.com", long, "expired"
		c.Rows[0].Label = long
	})
	v := BuildIcon(provider.Claude, in)
	if n := len(utf16.Encode([]rune(v.Tooltip))); n > 127 {
		t.Fatalf("tooltip is %d UTF-16 units", n)
	}
	got := truncateUTF16(strings.Repeat("é", 200), 127)
	if n := len(utf16.Encode([]rune(got))); n > 127 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%d units, %q", n, got)
	}
	got = truncateUTF16(strings.Repeat("\U0001F600", 100), 127) // surrogate pairs are not split
	if n := len(utf16.Encode([]rune(got))); n > 127 || strings.ContainsRune(got, 0xFFFD) {
		t.Fatalf("%d units", n)
	}
}

func TestTooltipMaxIsWindowsLimit(t *testing.T) {
	eq(t, tooltipMax, 127)
	got := truncateUTF16(strings.Repeat("x", 300), tooltipMax)
	eq(t, len(utf16.Encode([]rune(got))), 127)
	eq(t, strings.HasSuffix(got, "…"), true)
}

func TestTitleShownPerOS(t *testing.T) {
	eq(t, ShowsTitle("linux"), true)
	eq(t, ShowsTitle("darwin"), true)
	eq(t, ShowsTitle("windows"), false)
}

func TestLinuxLabelText(t *testing.T) {
	label := func(in MenuInputs) string { return BuildIcon(provider.Claude, in).Title }
	in := base()
	eq(t, label(in), "71%")
	setWeekly(&in, pf(99.6), "normal")
	eq(t, label(in), "100%")
	in = base()
	set(&in, func(c *status.ProviderStatus) { c.LatestFetchedAt = now.Add(-16 * time.Minute) })
	eq(t, label(in), "71% ⚠")
	in = base()
	setWeekly(&in, nil, "normal")
	eq(t, label(in), "–%")
	// the guide is at least as wide as any label
	for _, s := range []string{"100%", "100% ⚠", "–% ⚠"} {
		if len([]rune(LabelGuide)) < len([]rune(s)) {
			t.Fatalf("guide %q shorter than %q", LabelGuide, s)
		}
	}
}

// The mascot stands for a detected sign-in: Clawd with a Claude Code
// sign-in (also an expired one: the account is there), the neutral ring
// without one or before the first status.
func TestViewProviderFollowsSignIn(t *testing.T) {
	for state, want := range map[string]string{"ok": "claude", "expired": "claude", "not_signed_in": "", "": ""} {
		in := base()
		set(&in, func(c *status.ProviderStatus) { c.State = state })
		if got := BuildIcon(provider.Claude, in).Icon.Provider; got != want {
			t.Fatalf("auth %q: provider %q, want %q", state, got, want)
		}
	}
}

func TestNumberInIcon(t *testing.T) {
	for _, c := range []struct {
		goos, desktop string
		want          bool
	}{
		{"windows", "", true},
		{"darwin", "", false},
		{"linux", "ubuntu:GNOME", false},
		{"linux", "GNOME", false},
		{"linux", "Unity", false},
		{"linux", "KDE", true},
		{"linux", "XFCE", true},
		{"linux", "", true}, // unknown: keep the number visible
	} {
		if got := NumberInIcon(c.goos, c.desktop); got != c.want {
			t.Fatalf("%s %q: %v, want %v", c.goos, c.desktop, got, c.want)
		}
	}
}

func TestSectionsShown(t *testing.T) {
	in := base()
	eq(t, SectionsShown(in.Snap), []provider.ID{provider.Claude})
	setP(&in, provider.Codex, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	eq(t, SectionsShown(in.Snap), []provider.ID{provider.Claude})
	setP(&in, provider.Codex, func(c *status.ProviderStatus) { c.State = "api_key" }) // a problem to show, no icon
	eq(t, SectionsShown(in.Snap), []provider.ID{provider.Claude, provider.Codex})
	eq(t, InUse(in.Snap.Provider(provider.Codex)), false)
	eq(t, InUse(in.Snap.Provider(provider.Claude)), true)
}
