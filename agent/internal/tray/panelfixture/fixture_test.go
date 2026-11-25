package panelfixture

import (
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// The fixture is the approved picture's input (PR-20): a Friday at 19:00
// UTC, both providers shown, the reset texts of the mock.
func TestFixtureMatchesTheMock(t *testing.T) {
	if Now.Weekday() != time.Friday || Now.Location() != time.UTC || Now.Hour() != 19 {
		t.Fatalf("Now = %v", Now)
	}
	in := Combined()
	if !in.Now.Equal(Now) || !in.HasToggle || !in.AutostartOn || in.DashboardURL != "https://usage.example.com" || in.Snap.Mode != "coordinated" {
		t.Fatalf("%+v", in)
	}
	var shown []string
	for _, it := range tray.BuildMenu(in) {
		if it.Visible {
			shown = append(shown, it.Text)
		}
	}
	want := []string{
		"Claude · team-a@… · 3 PCs",
		"5-hour · Fri 21:20 (2h 20m)", "Weekly · Sun 01:00 (1d 6h)", "Fable · Sun 01:00 (1d 6h)",
		"Codex · bob@… · 1 PC · Plus",
		"5-hour blocked · Fri 19:35 (35m)", "Weekly · Sat 22:00 (1d 3h)",
		"Server connected · Updated 18:52 · this PC fetches Claude, Codex",
		"Open dashboard", "Refresh now", "Start at login", "Remove AI Usage", "Quit",
	}
	if len(shown) != len(want) {
		t.Fatalf("menu %q", shown)
	}
	for i := range want {
		if !strings.HasSuffix(shown[i], want[i]) {
			t.Fatalf("item %d: %q, want …%q", i, shown[i], want[i])
		}
	}
	for _, c := range []struct {
		id   provider.ID
		pcts []float64
	}{{provider.Claude, []float64{42, 13, 7}}, {provider.Codex, []float64{100, 8}}} {
		ps := in.Snap.Providers[c.id]
		for i, r := range ps.Rows {
			if *r.Percent != c.pcts[i] || r.Level != usage.LevelOf(usage.Limit{Percent: r.Percent, Severity: "normal"}) {
				t.Fatalf("%s row %d: %+v", c.id, i, r)
			}
		}
	}
	// the icons show Claude's 5-hour and Codex's blocked 5-hour, as the mock's top bar
	if v := tray.BuildIcon(provider.Claude, in); v.Title != "42%" {
		t.Fatalf("%+v", v)
	}
	if v := tray.BuildIcon(provider.Codex, in); v.Title != "100%" {
		t.Fatalf("%+v", v)
	}
}

// States is Combined with Claude's numbers stale and Codex's sign-in
// expired (its last rows kept); each call gives a fresh copy.
func TestStatesFixture(t *testing.T) {
	s := States()
	c, x := s.Snap.Providers[provider.Claude], s.Snap.Providers[provider.Codex]
	if !usage.Stale(c.LatestFetchedAt, s.Now) || c.LatestFetchedAt.Format("15:04") != "18:30" {
		t.Fatalf("claude %v", c.LatestFetchedAt)
	}
	if x.State != "expired" || len(x.Rows) != 2 || usage.Stale(x.LatestFetchedAt, s.Now) {
		t.Fatalf("codex %+v", x)
	}
	// a change to one copy does not reach the next
	c.Rows[0].Label = "changed"
	s.Snap.Providers[provider.Claude] = c
	if Combined().Snap.Providers[provider.Claude].Rows[0].Label != "5-hour" || States().Snap.Providers[provider.Claude].Rows[0].Label != "5-hour" {
		t.Fatal("the fixtures share their rows")
	}
}
