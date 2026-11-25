// Package panelfixture is the fixed input of the usage panel's pictures
// and checks (plan PR-20): the approved mock's values on all three OSes,
// the fake panel tool and the tests. All values are test data.
package panelfixture

import (
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// Now is the fixture's time: a Friday, 19:00 UTC.
var Now = time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC)

// at is a time on the fixture's day, d days later.
func at(d, h, m int) *time.Time {
	t := time.Date(2026, 11, 27+d, h, m, 0, 0, time.UTC)
	return &t
}

func pct(v float64) *float64 { return &v }

// row is a limit row with the level the providers store for it.
func row(key, label string, p float64, resets *time.Time, mins int) provider.Row {
	return provider.Row{Key: key, Label: label, Percent: pct(p), ResetsAt: resets, WindowMins: mins,
		Level: usage.LevelOf(usage.Limit{Percent: pct(p), Severity: "normal"})}
}

// Combined is the mock (panel-combined.png): Claude team-a@example.com on
// 3 PCs, 5-hour 42 % (resets Fri 21:20), Weekly 13 % and Fable 7 % (Sun
// 01:00), fetched 18:52; Codex bob@example.com on 1 PC, Plus, 5-hour 100 %
// blocked (Fri 19:35), Weekly 8 % (Sat 22:00), fetched 18:52; the server
// connected, this PC fetches both; Start at login on; the dashboard at
// https://usage.example.com. Each call gives a fresh copy.
func Combined() tray.MenuInputs {
	fetched := time.Date(2026, 11, 27, 18, 52, 0, 0, time.UTC)
	five := row("codex:5h", "5-hour", 100, at(0, 19, 35), 300)
	five.Blocked = true
	return tray.MenuInputs{
		Snap: status.Snapshot{Mode: "coordinated", ServerURL: "https://usage.example.com",
			Providers: map[provider.ID]status.ProviderStatus{
				provider.Claude: {State: "ok", AccountEmail: "team-a@example.com", AccountKey: "acc-0001", PeersPCs: 3,
					Role: "poller", LatestFetchedAt: fetched, MainKey: "five_hour", Rows: []provider.Row{
						row("five_hour", "5-hour", 42, at(0, 21, 20), 300),
						row("weekly_all", "Weekly", 13, at(2, 1, 0), 10080),
						row("weekly_scoped:Fable", "Fable", 7, at(2, 1, 0), 10080),
					}},
				provider.Codex: {State: "ok", AccountEmail: "bob@example.com", AccountKey: "codex:acc-0002", Plan: "plus",
					PeersPCs: 1, Role: "poller", LatestFetchedAt: fetched, MainKey: "codex:weekly", Rows: []provider.Row{
						five,
						row("codex:weekly", "Weekly", 8, at(1, 22, 0), 10080),
					}},
			}},
		Now: Now, Version: "0.9.1", HasToggle: true, AutostartOn: true, DashboardURL: "https://usage.example.com",
	}
}

// States is Combined with Claude's numbers fetched at 18:30 (stale) and
// Codex's sign-in expired (its last rows kept).
func States() tray.MenuInputs {
	in := Combined()
	c := in.Snap.Providers[provider.Claude]
	c.LatestFetchedAt = time.Date(2026, 11, 27, 18, 30, 0, 0, time.UTC)
	in.Snap.Providers[provider.Claude] = c
	x := in.Snap.Providers[provider.Codex]
	x.State = "expired"
	in.Snap.Providers[provider.Codex] = x
	return in
}
