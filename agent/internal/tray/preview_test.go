package tray

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// TestMenuPreview prints the menu of a few situations as JSON lines
// ("PREVIEW {…}") for a person to render and look at; it runs only with
// AI_USAGE_MENU_PREVIEW=1. All values are fake (test data only).
func TestMenuPreview(t *testing.T) {
	if os.Getenv("AI_USAGE_MENU_PREVIEW") != "1" {
		t.Skip("set AI_USAGE_MENU_PREVIEW=1 to print the preview data")
	}
	fiveHour := func(in *MenuInputs, pct float64) {
		soon := now.Add(2*time.Hour + 20*time.Minute).UTC()
		set(in, func(c *status.ProviderStatus) {
			c.Rows = append([]provider.Row{{Key: "five_hour", Label: "5-hour", Percent: pf(pct), ResetsAt: &soon,
				Level: usage.LevelOf(usage.Limit{Percent: pf(pct), Severity: "normal"}), WindowMins: 300}}, c.Rows...)
		})
	}
	type scenario struct {
		name string
		in   MenuInputs
	}
	var list []scenario
	in := base()
	list = append(list, scenario{"claude-weekly-fable", in})
	in = base()
	fiveHour(&in, 45)
	list = append(list, scenario{"claude-5hour-weekly-fable", in})
	in = base()
	set(&in, func(c *status.ProviderStatus) { c.LatestFetchedAt = now.Add(-47 * time.Minute) })
	in.Snap.Mode = "standalone"
	list = append(list, scenario{"claude-stale", in})
	in = base()
	set(&in, func(c *status.ProviderStatus) { c.State = "not_signed_in" })
	list = append(list, scenario{"signed-out", in})
	in = base()
	set(&in, func(c *status.ProviderStatus) { c.State = "expired" })
	list = append(list, scenario{"claude-expired", in})
	in = base()
	fiveHour(&in, 100)
	r5 := now.Add(35 * time.Minute).UTC()
	rw := now.Add(27 * time.Hour).UTC()
	setP(&in, provider.Codex, func(c *status.ProviderStatus) {
		*c = status.ProviderStatus{State: "ok", AccountEmail: "bob@example.com", AccountKey: "codex:a:b", Plan: "plus", PeersPCs: 1,
			Role: "poller", LatestFetchedAt: now.Add(-time.Minute).UTC(), MainKey: "codex:weekly", Rows: []provider.Row{
				{Key: "codex:5h", Label: "5-hour", Percent: pf(100), Blocked: true, ResetsAt: &r5, Level: usage.LevelCritical, WindowMins: 300},
				{Key: "codex:weekly", Label: "Weekly", Percent: pf(8), ResetsAt: &rw, Level: usage.LevelNormal, WindowMins: 10080},
			}}
	})
	list = append(list, scenario{"two-providers", in})

	type line struct {
		ID       string `json:"id"`
		Text     string `json:"text"`
		Image    string `json:"image,omitempty"`
		Enabled  bool   `json:"enabled"`
		Checkbox bool   `json:"checkbox,omitempty"`
		Checked  bool   `json:"checked,omitempty"`
		Sep      bool   `json:"sep,omitempty"`
	}
	for _, sc := range list {
		items := BuildMenu(sc.in)
		sections := map[string]bool{}
		for _, it := range items {
			if s := trayproto.Section(it.ID); s != "" && it.Visible {
				sections[s] = true
			}
		}
		var lines []line
		for _, it := range items {
			if it.Visible {
				lines = append(lines, line{ID: it.ID, Text: it.Text, Image: it.Image, Enabled: it.Enabled, Checkbox: it.Checkbox, Checked: it.Checked})
			}
			if s := trayproto.Section(it.ID); s != "" && it.ID == "p."+s && sections[s] || it.ID == "server" {
				lines = append(lines, line{Sep: true})
			}
		}
		icons := map[string]any{}
		ids := SectionsShown(sc.in.Snap)
		if len(ids) == 0 {
			ids = []provider.ID{""}
		}
		for _, id := range ids {
			if id != "" && !InUse(sc.in.Snap.Providers[id]) {
				continue
			}
			v := BuildIcon(id, sc.in)
			spec := icon.Spec{Provider: icon.Provider(v.Icon.Provider), Percent: v.Icon.Percent, Level: usage.Level(v.Icon.Level), Stale: v.Icon.Stale}
			withNumber := spec
			withNumber.Number = true
			icons[string(id)] = map[string]any{"view": v,
				"gnome32": icon.PNG(icon.Tray(spec, 32)), "windows16": icon.PNG(icon.Tray(withNumber, 16)),
				"windows24": icon.PNG(icon.Tray(withNumber, 24)), "menu32": icon.MenuImage("linux", spec.Provider)}
		}
		b, err := json.Marshal(map[string]any{"name": sc.name, "lines": lines, "icons": icons})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PREVIEW %s\n", b)
	}
}
