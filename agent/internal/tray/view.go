// Package tray is the tray icon and its menu. view.go is the pure part:
// which texts, icon and items to show for a status snapshot.
package tray

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/icon"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/usage"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/version"
)

// ItemID names a menu item; the values are in display order.
type ItemID int

const (
	ItemNewVersion ItemID = iota
	ItemWeekly
	ItemScoped
	ItemSignIn
	ItemAccount
	ItemServer
	ItemDashboard
	ItemRefresh
	ItemAutostart
	ItemRemove
	ItemQuit
	itemCount
)

// Item is one menu row.
type Item struct {
	ID                        ItemID
	Text                      string
	Enabled, Visible, Checked bool
}

// View is everything the tray shows.
type View struct {
	Icon    icon.Spec
	Title   string // the text next to the icon (macOS only)
	Tooltip string
	Items   []Item
}

// Inputs are what the view depends on.
type Inputs struct {
	Snap         status.Snapshot
	Now          time.Time
	Version      string
	HasToggle    bool
	AutostartOn  bool
	RemoveArmed  bool
	DashboardURL string
}

// tooltipMax is in UTF-16 units (Windows NOTIFYICONDATA szTip holds 128
// with the NUL).
const tooltipMax = 127

const sep = text.Sep

// Build turns the inputs into a view. Times are shown in in.Now's location.
func Build(in Inputs) View {
	s, now := in.Snap, in.Now
	loc := now.Location()
	signedOut := s.AuthState == "not_signed_in"

	weekly, scoped := s.WeeklyPercent, s.ScopedPercent
	if signedOut {
		weekly, scoped = nil, nil
	}
	sev := s.WeeklySeverity
	if sev == "" {
		sev = "normal"
	}
	stale := !s.LatestFetchedAt.IsZero() && usage.Stale(s.LatestFetchedAt, now)
	v := View{Icon: icon.Spec{Percent: weekly, Level: usage.LevelOf(usage.Limit{Percent: weekly, Severity: sev}), Stale: stale}}
	v.Title = usage.FormatPercent(weekly)
	if stale {
		v.Title += text.StaleMark
	}

	items := make([]Item, itemCount)
	for i := range items {
		items[i] = Item{ID: ItemID(i), Visible: true, Enabled: true}
	}
	info := func(id ItemID, text string, show bool) {
		items[id] = Item{ID: id, Text: text, Visible: show}
	}
	resetOf := func(t time.Time) string {
		if t.IsZero() {
			return usage.FormatReset(nil, now)
		}
		r := t.In(loc)
		return usage.FormatReset(&r, now)
	}

	newer := version.Newer(s.ClientLatestVersion, in.Version)
	items[ItemNewVersion] = Item{ID: ItemNewVersion, Text: text.NewVersion + s.ClientLatestVersion, Visible: newer, Enabled: newer}

	weeklyText := text.AllModels + "  " + usage.FormatPercent(weekly)
	weeklyTip := text.AllModels + " " + usage.FormatPercent(weekly)
	if weekly != nil {
		weeklyText += "  " + resetOf(s.WeeklyResetsAt)
		weeklyTip += " · " + resetOf(s.WeeklyResetsAt)
	}
	info(ItemWeekly, weeklyText, true)
	scopedText := ""
	if scoped != nil {
		scopedText = usage.ScopedModel + "  " + usage.FormatPercent(scoped) + "  " + resetOf(s.ScopedResetsAt)
	}
	info(ItemScoped, scopedText, scoped != nil)

	signIn := ""
	switch s.AuthState {
	case "not_signed_in":
		signIn = text.SignInNeeded
	case "expired":
		signIn = usage.FetchErrorText(401)
	}
	info(ItemSignIn, signIn, signIn != "")

	account := ""
	if s.AccountEmail != "" {
		local, _, _ := strings.Cut(s.AccountEmail, "@")
		account = text.Account + local + "@…"
		switch {
		case s.PeersDevices == 1:
			account += sep + text.OnePC
		case s.PeersDevices >= 2:
			account += sep + strconv.Itoa(s.PeersDevices) + text.PCsSuffix
		}
	}
	info(ItemAccount, account, account != "")

	updated := ""
	if !s.LatestFetchedAt.IsZero() {
		updated = usage.FormatUpdated(s.LatestFetchedAt, now)
	}
	var server string
	switch s.Mode {
	case "coordinated":
		server = text.Connected + sep
		if updated != "" {
			server += updated
		} else {
			server += text.NoUsageYet
		}
		if s.Role == "poller" {
			server += sep + text.ThisPCFetches
		} else if s.PollerHostname != "" {
			server += sep + text.Poller + s.PollerHostname
		}
	case "standalone":
		server = text.Unreachable
		if updated != "" {
			server += sep + updated
		}
	default:
		server = text.Connecting
	}
	info(ItemServer, server, true)

	items[ItemDashboard].Text = text.Dashboard
	items[ItemDashboard].Enabled = in.DashboardURL != ""
	items[ItemRefresh].Text = text.Refresh
	if s.FreshPending {
		items[ItemRefresh].Text, items[ItemRefresh].Enabled = text.Refreshing, false
	}
	items[ItemAutostart] = Item{ID: ItemAutostart, Text: text.Autostart, Visible: in.HasToggle, Enabled: in.HasToggle, Checked: in.AutostartOn}
	items[ItemRemove].Text = text.Remove
	if in.RemoveArmed {
		items[ItemRemove].Text = text.RemoveArmed
	}
	items[ItemQuit].Text = text.Quit
	v.Items = items

	tip := []string{text.App, weeklyTip}
	if scoped != nil {
		tip = append(tip, usage.ScopedModel+" "+usage.FormatPercent(scoped))
	}
	if signIn != "" {
		tip = append(tip, signIn)
	} else if updated != "" {
		tip = append(tip, updated)
	}
	v.Tooltip = truncateUTF16(strings.Join(tip, "\n"), tooltipMax)
	return v
}

// truncateUTF16 shortens s to at most n UTF-16 units, ending with "…" when
// it had to cut. A surrogate pair is never split.
func truncateUTF16(s string, n int) string {
	if len(utf16.Encode([]rune(s))) <= n {
		return s
	}
	units := 0
	var b strings.Builder
	for _, r := range s {
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1
		}
		if units+w > n-1 { // room for "…"
			break
		}
		units += w
		b.WriteRune(r)
	}
	return b.String() + "…"
}
