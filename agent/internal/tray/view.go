// Package tray is what the tray icons and their menu show for a status
// snapshot (the helpers in package trayhelper draw it), and opening a web
// page from the menu.
package tray

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
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
	Title   string // the text next to the icon (macOS, Linux); see NextToIcon
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

// LabelGuide is the widest text next to the icon, for Linux tray hosts
// that reserve its room (XAyatanaLabelGuide).
const LabelGuide = "100%" + text.StaleMark

// NextToIcon is the text shown next to the tray icon on goos, and whether
// that OS shows one: the menu bar title on macOS, the label in Ubuntu's top
// bar (AppIndicator) on Linux. Windows shows none. The label is set on
// every Linux desktop; those that ignore it get the number in the icon
// (NumberInIcon).
func NextToIcon(goos string, v View) (string, bool) {
	switch goos {
	case "darwin", "linux":
		return v.Title, true
	}
	return "", false
}

// NumberInIcon reports whether the tray icon must carry the number itself:
// on Windows (no text next to tray icons) and on Linux desktops other than
// GNOME and Unity, whose AppIndicator hosts show the label. desktop is
// $XDG_CURRENT_DESKTOP (a colon-separated list); when it is unknown the
// number stays in the icon.
func NumberInIcon(goos, desktop string) bool {
	switch goos {
	case "windows":
		return true
	case "linux":
		for _, d := range strings.Split(desktop, ":") {
			if strings.EqualFold(d, "GNOME") || strings.EqualFold(d, "Unity") {
				return false
			}
		}
		return true
	}
	return false
}

// IconImage is the tray icon file for v on goos and Linux desktop.
func IconImage(goos, desktop string, v View) []byte {
	s := v.Icon
	s.Number = NumberInIcon(goos, desktop)
	return icon.TrayImage(goos, s)
}

// tooltipMax is in UTF-16 units (Windows NOTIFYICONDATA szTip holds 128
// with the NUL).
const tooltipMax = 127

const sep = text.Sep

// Build turns the inputs into a view. Times are shown in in.Now's location.
func Build(in Inputs) View {
	s, now := in.Snap, in.Now
	loc := now.Location()
	c := s.Provider(provider.Claude)
	signedOut := c.State == "not_signed_in"

	weeklyRow, _ := c.Row("weekly_all")
	scopedRow, _ := c.Row("weekly_scoped:" + usage.ScopedModel)
	weekly, scoped := weeklyRow.Percent, scopedRow.Percent
	if signedOut {
		weekly, scoped = nil, nil
	}
	level := weeklyRow.Level
	switch {
	case weekly == nil:
		level = usage.LevelNone
	case level == "":
		level = usage.LevelOf(usage.Limit{Percent: weekly, Severity: "normal"})
	}
	stale := !c.LatestFetchedAt.IsZero() && usage.Stale(c.LatestFetchedAt, now)
	v := View{Icon: icon.Spec{Percent: weekly, Level: level, Stale: stale}}
	// The mascot stands for a detected Claude Code sign-in (an expired one
	// too: the account is there). Before the first status, or signed out,
	// the icon is the neutral ring.
	if c.State == "ok" || c.State == "expired" {
		v.Icon.Provider = icon.Claude
	}
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
	resetOf := func(t *time.Time) string {
		if t == nil || t.IsZero() {
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
		weeklyText += "  " + resetOf(weeklyRow.ResetsAt)
		weeklyTip += " · " + resetOf(weeklyRow.ResetsAt)
	}
	info(ItemWeekly, weeklyText, true)
	scopedText := ""
	if scoped != nil {
		scopedText = usage.ScopedModel + "  " + usage.FormatPercent(scoped) + "  " + resetOf(scopedRow.ResetsAt)
	}
	info(ItemScoped, scopedText, scoped != nil)

	signIn := ""
	switch c.State {
	case "not_signed_in":
		signIn = text.SignInNeeded
	case "expired":
		signIn = usage.FetchErrorText(401)
	}
	info(ItemSignIn, signIn, signIn != "")

	account := ""
	if c.AccountEmail != "" {
		local, _, _ := strings.Cut(c.AccountEmail, "@")
		account = text.Account + local + "@…"
		switch {
		case c.PeersPCs == 1:
			account += sep + text.OnePC
		case c.PeersPCs >= 2:
			account += sep + strconv.Itoa(c.PeersPCs) + text.PCsSuffix
		}
	}
	info(ItemAccount, account, account != "")

	updated := ""
	if !c.LatestFetchedAt.IsZero() {
		updated = usage.FormatUpdated(c.LatestFetchedAt, now)
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
		if c.Role == "poller" {
			server += sep + text.ThisPCFetches
		} else if c.PollerHostname != "" {
			server += sep + text.Poller + c.PollerHostname
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

// protoIDs are the helper protocol's ids of the view's items.
var protoIDs = map[ItemID]string{
	ItemNewVersion: "newversion", ItemAccount: "h.claude", ItemWeekly: "r.claude.0", ItemScoped: "r.claude.1",
	ItemSignIn: "p.claude", ItemServer: "server", ItemDashboard: "dashboard", ItemRefresh: "refresh",
	ItemAutostart: "autostart", ItemRemove: "remove", ItemQuit: "quit",
}

// ItemOf is the view's item for a protocol id (false for ids it has none
// for).
func ItemOf(id string) (ItemID, bool) {
	for k, v := range protoIDs {
		if v == id {
			return k, true
		}
	}
	return 0, false
}

// Menu is v's menu as the helpers get it: every trayproto.ItemIDs item in
// order; those the view has none for are hidden.
func Menu(v View) []trayproto.Item {
	byID := map[string]Item{}
	for _, it := range v.Items {
		byID[protoIDs[it.ID]] = it
	}
	out := make([]trayproto.Item, 0, len(trayproto.ItemIDs))
	for _, id := range trayproto.ItemIDs {
		it, ok := byID[id]
		p := trayproto.Item{ID: id}
		if ok {
			p.Text, p.Enabled, p.Visible, p.Checked = it.Text, it.Enabled, it.Visible, it.Checked
			p.Checkbox = it.ID == ItemAutostart
		}
		out = append(out, p)
	}
	return out
}

// IconView is v's icon, title and tooltip as one helper gets them.
func IconView(v View) supervisor.IconView {
	return supervisor.IconView{
		Icon:  trayproto.Icon{Provider: string(v.Icon.Provider), Percent: v.Icon.Percent, Level: string(v.Icon.Level), Stale: v.Icon.Stale},
		Title: v.Title, Tooltip: v.Tooltip,
	}
}
