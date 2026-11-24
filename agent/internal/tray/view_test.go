package tray

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/usage"
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

func full() status.Snapshot {
	return status.Snapshot{Mode: "coordinated", Role: "standby", AuthState: "ok", AccountEmail: "team-a@example.com",
		PeersDevices: 3, PollerHostname: "TEST-PC-03", LatestFetchedAt: now.Add(-2 * time.Minute).UTC(),
		WeeklyPercent: pf(71), WeeklyResetsAt: now.Add(30 * time.Hour).UTC(), WeeklySeverity: "normal",
		ScopedPercent: pf(76), ScopedResetsAt: now.Add(30 * time.Hour).UTC(), ScopedSeverity: "warning"}
}

func item(t *testing.T, v View, id ItemID) Item {
	t.Helper()
	for _, it := range v.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %d", id)
	return Item{}
}

func textOf(v View, id ItemID) string {
	for _, it := range v.Items {
		if it.ID == id {
			return it.Text
		}
	}
	return "<missing>"
}

func visible(v View, id ItemID) bool {
	for _, it := range v.Items {
		if it.ID == id {
			return it.Visible
		}
	}
	return false
}

func checked(v View, id ItemID) bool {
	for _, it := range v.Items {
		if it.ID == id {
			return it.Checked
		}
	}
	return false
}

func base() Inputs {
	return Inputs{Snap: full(), Now: now, Version: "1.0.0", HasToggle: true, AutostartOn: true, DashboardURL: "http://claude-usage.local"}
}

func TestViewFull(t *testing.T) {
	v := Build(base())
	eq(t, textOf(v, ItemWeekly), "All models  71%  Resets Sun 01:00 · in 1d 6h")
	eq(t, textOf(v, ItemScoped), "Fable  76%  Resets Sun 01:00 · in 1d 6h")
	eq(t, textOf(v, ItemAccount), "Account team-a@…  ·  3 PCs on this account")
	eq(t, textOf(v, ItemServer), "Server connected  ·  Updated 18:58  ·  poller TEST-PC-03")
	eq(t, v.Title, "71%")
	eq(t, v.Icon.Level, usage.LevelNormal)
	eq(t, *v.Icon.Percent, 71.0)
	eq(t, v.Icon.Stale, false)
	eq(t, checked(v, ItemAutostart), true)
	eq(t, v.Tooltip, "Claude Usage\nAll models 71% · Resets Sun 01:00 · in 1d 6h\nFable 76%\nUpdated 18:58")
	eq(t, visible(v, ItemSignIn), false)
	eq(t, visible(v, ItemNewVersion), false)
	// order, and which rows are information only
	var ids []ItemID
	for _, it := range v.Items {
		ids = append(ids, it.ID)
	}
	eq(t, ids, []ItemID{ItemNewVersion, ItemWeekly, ItemScoped, ItemSignIn, ItemAccount, ItemServer, ItemDashboard, ItemRefresh, ItemAutostart, ItemRemove, ItemQuit})
	for _, id := range []ItemID{ItemWeekly, ItemScoped, ItemSignIn, ItemAccount, ItemServer} {
		eq(t, item(t, v, id).Enabled, false)
	}
	for _, id := range []ItemID{ItemDashboard, ItemRefresh, ItemAutostart, ItemRemove, ItemQuit} {
		eq(t, item(t, v, id).Enabled, true)
		eq(t, item(t, v, id).Visible, true)
	}
	eq(t, textOf(v, ItemDashboard), "Open dashboard")
	eq(t, textOf(v, ItemRefresh), "Refresh now")
	eq(t, textOf(v, ItemAutostart), "Start at login")
	eq(t, textOf(v, ItemRemove), "Remove Claude Usage")
	eq(t, textOf(v, ItemQuit), "Quit")
}

func TestViewNotSignedInShowsDash(t *testing.T) {
	in := base()
	in.Snap.AuthState = "not_signed_in"
	v := Build(in)
	eq(t, v.Title, "–%")
	eq(t, v.Icon.Percent == nil, true)
	eq(t, v.Icon.Level, usage.LevelNone)
	eq(t, textOf(v, ItemWeekly), "All models  –%")
	eq(t, visible(v, ItemSignIn), true)
	eq(t, textOf(v, ItemSignIn), "Claude Code sign-in needed")
	eq(t, visible(v, ItemScoped), false)
	eq(t, v.Tooltip, "Claude Usage\nAll models –%\nClaude Code sign-in needed")
}

func TestViewExpiredKeepsNumbers(t *testing.T) {
	in := base()
	in.Snap.AuthState = "expired"
	v := Build(in)
	eq(t, v.Title, "71%")
	eq(t, textOf(v, ItemSignIn), "Sign-in expired: open Claude Code to renew it")
	eq(t, visible(v, ItemSignIn), true)
	eq(t, visible(v, ItemScoped), true)
}

func TestViewStale(t *testing.T) {
	in := base()
	in.Snap.LatestFetchedAt = now.Add(-16 * time.Minute)
	v := Build(in)
	eq(t, v.Title, "71% ⚠")
	eq(t, v.Icon.Stale, true)
	in.Snap.LatestFetchedAt = now.Add(-14 * time.Minute)
	v = Build(in)
	eq(t, v.Title, "71%")
	eq(t, v.Icon.Stale, false)
}

func TestViewWarningFlag(t *testing.T) {
	in := base()
	in.Snap.WeeklyPercent, in.Snap.WeeklySeverity = pf(40), "warning"
	eq(t, Build(in).Icon.Level, usage.LevelWarning)
	in.Snap.WeeklySeverity = "" // an old status file without severity reads as normal
	eq(t, Build(in).Icon.Level, usage.LevelNormal)
	in.Snap.WeeklyPercent = pf(92)
	eq(t, Build(in).Icon.Level, usage.LevelCritical)
}

func TestViewStandaloneAndStarting(t *testing.T) {
	in := base()
	in.Snap.Mode = "standalone"
	eq(t, textOf(Build(in), ItemServer), "Server not reachable  ·  this PC fetches every 30 min  ·  Updated 18:58")
	in.Snap.LatestFetchedAt = time.Time{}
	eq(t, textOf(Build(in), ItemServer), "Server not reachable  ·  this PC fetches every 30 min")
	in.Snap.Mode = "starting"
	eq(t, textOf(Build(in), ItemServer), "Connecting to the server…")
	in.Snap.Mode = ""
	eq(t, textOf(Build(in), ItemServer), "Connecting to the server…")
	in.Snap.Mode = "coordinated"
	eq(t, textOf(Build(in), ItemServer), "Server connected  ·  no usage yet  ·  poller TEST-PC-03")
}

func TestViewPollerSelf(t *testing.T) {
	in := base()
	in.Snap.Role = "poller"
	eq(t, textOf(Build(in), ItemServer), "Server connected  ·  Updated 18:58  ·  this PC fetches")
	in.Snap.Role, in.Snap.PollerHostname = "standby", ""
	eq(t, textOf(Build(in), ItemServer), "Server connected  ·  Updated 18:58")
}

func TestViewAccount(t *testing.T) {
	in := base()
	in.Snap.PeersDevices = 1
	eq(t, textOf(Build(in), ItemAccount), "Account team-a@…  ·  1 PC on this account")
	in.Snap.PeersDevices = 0
	eq(t, textOf(Build(in), ItemAccount), "Account team-a@…")
	in.Snap.AccountEmail = ""
	eq(t, visible(Build(in), ItemAccount), false)
}

func TestViewNewVersion(t *testing.T) {
	in := base()
	in.Snap.ClientLatestVersion = "1.1.0"
	v := Build(in)
	eq(t, visible(v, ItemNewVersion), true)
	eq(t, textOf(v, ItemNewVersion), "New version available: 1.1.0")
	eq(t, item(t, v, ItemNewVersion).Enabled, true)
	in.Version = "0.0.0-dev"
	eq(t, visible(Build(in), ItemNewVersion), false)
	in.Version = "1.1.0"
	eq(t, visible(Build(in), ItemNewVersion), false)
}

func TestViewRemoveArmedAndRefreshing(t *testing.T) {
	in := base()
	in.RemoveArmed = true
	in.Snap.FreshPending = true
	v := Build(in)
	eq(t, textOf(v, ItemRemove), "Click again to remove Claude Usage")
	eq(t, textOf(v, ItemRefresh), "Refreshing…")
	eq(t, item(t, v, ItemRefresh).Enabled, false)
	in.HasToggle, in.DashboardURL = false, ""
	v = Build(in)
	eq(t, visible(v, ItemAutostart), false)
	eq(t, item(t, v, ItemDashboard).Enabled, false)
}

func TestViewUsesNowLocation(t *testing.T) {
	in := base()
	in.Now = now.UTC()
	eq(t, textOf(Build(in), ItemServer), "Server connected  ·  Updated 09:58  ·  poller TEST-PC-03")
}

func TestTooltipLimit(t *testing.T) {
	in := base()
	long := strings.Repeat("x", 300)
	in.Snap.AccountEmail = long + "@example.com"
	in.Snap.PollerHostname = long
	in.Snap.AuthState = "expired"
	v := Build(in)
	if n := len(utf16.Encode([]rune(v.Tooltip))); n > 127 {
		t.Fatalf("tooltip is %d UTF-16 units", n)
	}
	got := truncateUTF16(strings.Repeat("\u00e9", 200), 127)
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
