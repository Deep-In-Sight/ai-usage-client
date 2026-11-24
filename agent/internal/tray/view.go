// Package tray is what the tray icons and their menu show for a status
// snapshot (the helpers in package trayhelper draw it), and opening a web
// page from the menu.
//
// The menu (design D4, 6.5) has one section per provider in use: a header
// with the provider's 16 px mascot, one row per limit with a text bar
// ("██████▌░░░  65%  Weekly · Sun 01:00 (1d 6h)") and a problem line. Every
// icon gets the same menu; each icon has its provider's own number.
package tray

import (
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

// MenuInputs are what the menu and the icons depend on.
type MenuInputs struct {
	Snap         status.Snapshot
	Now          time.Time // times are shown in its location
	Version      string
	HasToggle    bool
	AutostartOn  bool
	RemoveArmed  bool
	DashboardURL string
}

// Bar cells (design D4): three characters from WGL4 (Windows' basic set),
// so the Windows menu font most likely has them. If Windows shows them
// badly, U+25A0/U+25A1 (full/empty only) are the fall-back.
const (
	barFull  = "█" // U+2588
	barHalf  = "▌" // U+258C: a remaining 5 % or more of the next cell
	barEmpty = "░" // U+2591
	barCells = 10
	// figureSpace is as wide as a digit in most fonts (U+2007): it pads a
	// percent to the width of "100%" so the names after it line up in the
	// menu's proportional font.
	figureSpace = " "
)

// Bar is a percentage as 10 text cells: "█" per 10 %, "▌" when 5 % or
// more remain, "░" for the rest; no value is all "░".
func Bar(p *float64) string {
	v := 0.0
	if p != nil {
		// the percent as the number next to the bar shows it
		// (usage.FormatPercent), so the two agree: 99.6 is a full bar
		// next to "100%"
		v = min(max(math.Floor(*p+0.5), 0), 100)
	}
	full := int(v / 10)
	var b strings.Builder
	b.WriteString(strings.Repeat(barFull, full))
	n := full
	if full < barCells && v-float64(full*10) >= 5 {
		b.WriteString(barHalf)
		n++
	}
	b.WriteString(strings.Repeat(barEmpty, barCells-n))
	return b.String()
}

// paddedPercent is usage.FormatPercent padded on the left with figure
// spaces to four characters ("100%").
func paddedPercent(p *float64) string {
	s := usage.FormatPercent(p)
	if n := len([]rune(s)); n < 4 {
		s = strings.Repeat(figureSpace, 4-n) + s
	}
	return s
}

// resetPart is " · Sun 16:00 (1d 6h)" ("" without a reset time).
func resetPart(r provider.Row, now time.Time) string {
	if r.ResetsAt == nil || r.ResetsAt.IsZero() {
		return ""
	}
	when, left, due := usage.ResetParts(*r.ResetsAt, now)
	if due {
		left = text.ResetDue
	}
	return text.Sep + when + " (" + left + ")"
}

func blocked(r provider.Row) string {
	if r.Blocked {
		return text.Blocked
	}
	return ""
}

// RowText is one limit's menu row: "<bar>  <percent>  <label>[ blocked] ·
// <day HH:MM> (<left>)", times in now's location.
func RowText(r provider.Row, now time.Time) string {
	return Bar(r.Percent) + "  " + paddedPercent(r.Percent) + "  " + r.Label + blocked(r) + resetPart(r, now)
}

// InUse: the provider's sign-in is there (an expired one too: the account
// is), so it has an icon (design 6.3, the v0.8 rule for Clawd).
func InUse(ps status.ProviderStatus) bool { return ps.State == "ok" || ps.State == "expired" }

// hasSection: the provider has something to show: a sign-in, or a problem
// with one (Codex in the keychain, an API key, a foreign server address).
func hasSection(ps status.ProviderStatus) bool { return ps.State != "" && ps.State != "not_signed_in" }

// order is the fixed order of the sections (design 6.5).
var order = []provider.ID{provider.Claude, provider.Codex}

// SectionsShown are the providers with a section, in menu order.
func SectionsShown(s status.Snapshot) []provider.ID {
	var out []provider.ID
	for _, id := range order {
		if ps, ok := s.Providers[id]; ok && hasSection(ps) {
			out = append(out, id)
		}
	}
	return out
}

// rowsFor is how many limit rows a provider's section has room for, and
// whether the last is kept for "More limits on the dashboard" when there
// are more (design D6: Codex shows four).
func rowsFor(id provider.ID) (rows int, more bool) {
	if id == provider.Codex {
		return trayproto.RowsPerSection - 1, true
	}
	return trayproto.RowsPerSection, false
}

// problem is the section's problem line ("" for none).
func problem(id provider.ID, ps status.ProviderStatus, s status.Snapshot) string {
	switch {
	case ps.State == "expired" && id == provider.Codex:
		return text.CodexExpired
	case ps.State == "expired":
		return text.ClaudeExpired
	case ps.State == "unsupported_store":
		return text.CodexKeychain
	case ps.State == "api_key":
		return text.CodexAPIKey
	case ps.State == "unsupported_base_url":
		return text.CodexBaseURL
	case ps.ParseProblem != "":
		return text.ReplyUnreadable
	case s.SchemaFallback && id != provider.Claude:
		return text.ServerNoCodex
	}
	return ""
}

// maxLocal is the most characters of an e-mail's local part the header
// shows; a longer one is cut, so the menu stays narrow.
const maxLocal = 20

// header is "<Provider> · <local>@… · <n> PCs" (Codex: " · <Plan>").
func header(id provider.ID, ps status.ProviderStatus) string {
	parts := []string{id.Name()}
	if ps.AccountEmail != "" {
		local, _, _ := strings.Cut(ps.AccountEmail, "@")
		if r := []rune(local); len(r) > maxLocal {
			local = string(r[:maxLocal-1]) + "…"
		}
		parts = append(parts, local+"@…")
	}
	switch {
	case ps.PeersPCs == 1:
		parts = append(parts, text.OnePC)
	case ps.PeersPCs >= 2:
		parts = append(parts, strconv.Itoa(ps.PeersPCs)+text.PCsSuffix)
	}
	if id == provider.Codex && ps.Plan != "" {
		parts = append(parts, strings.ToUpper(ps.Plan[:1])+ps.Plan[1:])
	}
	return strings.Join(parts, text.Sep)
}

// updated is the "Updated HH:MM" of the oldest numbers shown (with the
// stale mark when they are old), "" when there are none.
func updated(in MenuInputs, ids []provider.ID) string {
	var oldest time.Time
	for _, id := range ids {
		if t := in.Snap.Providers[id].LatestFetchedAt; !t.IsZero() && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return ""
	}
	u := usage.FormatUpdated(oldest, in.Now)
	if usage.Stale(oldest, in.Now) {
		u += text.StaleMark
	}
	return u
}

// serverLine is the server row: connection, the numbers' age, and who
// fetches for each provider shown.
func serverLine(in MenuInputs, ids []provider.ID) string {
	s := in.Snap
	u := updated(in, ids)
	switch s.Mode {
	case "coordinated":
		parts := []string{text.Connected}
		if len(ids) == 0 {
			return text.Connected // no account: no numbers to speak of
		}
		if u != "" {
			parts = append(parts, u)
		} else {
			parts = append(parts, text.NoUsageYet)
		}
		var mine, others []string
		for _, id := range ids {
			ps := s.Providers[id]
			switch {
			case ps.Role == "poller":
				mine = append(mine, id.Name())
			case ps.PollerHostname != "" && len(ids) == 1:
				others = append(others, text.Fetching+ps.PollerHostname)
			case ps.PollerHostname != "":
				others = append(others, id.Name()+text.FetchedFrom+ps.PollerHostname)
			}
		}
		if len(mine) > 0 {
			parts = append(parts, text.ThisPCFetches+strings.Join(mine, ", "))
		}
		return strings.Join(append(parts, others...), text.Sep)
	case "standalone":
		if u != "" {
			return text.Unreachable + text.Sep + u
		}
		return text.Unreachable
	}
	return text.Connecting
}

// AccountURL is the dashboard's page of an account.
func AccountURL(dashboard, accountKey string) string {
	return strings.TrimRight(dashboard, "/") + "/accounts/" + url.PathEscape(accountKey)
}

// RowURL is the page a click on a menu item opens: a limit row (or "More
// limits") its account's page, the dashboard when the account is not
// known; "" for any other item.
func RowURL(in MenuInputs, id string) string {
	sec := trayproto.Section(id)
	if sec == "" || !strings.HasPrefix(id, "r.") || in.DashboardURL == "" {
		return ""
	}
	ps := in.Snap.Providers[provider.ID(sec)]
	n, err := strconv.Atoi(id[strings.LastIndexByte(id, '.')+1:])
	if err != nil || n >= len(ps.Rows) {
		return ""
	}
	if ps.AccountKey == "" {
		return in.DashboardURL
	}
	return AccountURL(in.DashboardURL, ps.AccountKey)
}

// BuildMenu is the menu: every trayproto.ItemIDs item, in that order.
func BuildMenu(in MenuInputs) []trayproto.Item {
	s, now := in.Snap, in.Now
	byID := map[string]trayproto.Item{}
	put := func(it trayproto.Item) { byID[it.ID] = it }
	info := func(id, t string) { put(trayproto.Item{ID: id, Text: t, Visible: t != ""}) }

	newer := version.Newer(s.ClientLatestVersion, in.Version)
	put(trayproto.Item{ID: "newversion", Text: text.NewVersion + s.ClientLatestVersion, Visible: newer, Enabled: newer})

	ids := SectionsShown(s)
	for _, id := range ids {
		ps := s.Providers[id]
		p := string(id)
		put(trayproto.Item{ID: "h." + p, Text: header(id, ps), Image: p, Visible: true})
		room, more := rowsFor(id)
		for i, r := range ps.Rows {
			t := RowText(r, now)
			if i == room {
				if !more {
					break
				}
				t = text.MoreLimits // the rest are on the dashboard
			}
			put(trayproto.Item{ID: "r." + p + "." + strconv.Itoa(i), Text: t, Visible: true, Enabled: true})
			if i == room {
				break
			}
		}
		info("p."+p, problem(id, ps, s))
	}
	if len(ids) == 0 && len(s.Providers) > 0 {
		// nobody signed in: the prompt in Claude's place
		prompt := text.SignInClaude
		if _, ok := s.Providers[provider.Codex]; ok {
			prompt = text.SignInAny
		}
		info("p.claude", prompt)
	}
	info("server", serverLine(in, ids))

	put(trayproto.Item{ID: "dashboard", Text: text.Dashboard, Visible: true, Enabled: in.DashboardURL != ""})
	refresh := trayproto.Item{ID: "refresh", Text: text.Refresh, Visible: true, Enabled: true}
	if s.FreshPending {
		refresh.Text, refresh.Enabled = text.Refreshing, false
	}
	put(refresh)
	put(trayproto.Item{ID: "autostart", Text: text.Autostart, Visible: in.HasToggle, Enabled: in.HasToggle, Checkbox: true, Checked: in.AutostartOn})
	remove := trayproto.Item{ID: "remove", Text: text.Remove, Visible: true, Enabled: true}
	if in.RemoveArmed {
		remove.Text = text.RemoveArmed
	}
	put(remove)
	put(trayproto.Item{ID: "quit", Text: text.Quit, Visible: true, Enabled: true})

	out := make([]trayproto.Item, 0, len(trayproto.ItemIDs))
	for _, id := range trayproto.ItemIDs {
		it, ok := byID[id]
		if !ok {
			it = trayproto.Item{ID: id}
		}
		out = append(out, it)
	}
	return out
}

// shownRow is the row a provider's icon shows (design D7) and whether
// there is one.
func shownRow(ps status.ProviderStatus) (provider.Row, bool) {
	return provider.Shown(provider.Parsed{Rows: ps.Rows, MainKey: ps.MainKey})
}

// BuildIcon is the icon, title and tooltip of provider p's helper ("" is
// the neutral helper). A provider not in use shows the neutral ring.
func BuildIcon(p provider.ID, in MenuInputs) supervisor.IconView {
	ps, ok := in.Snap.Providers[p]
	tip := []string{text.App}
	var v supervisor.IconView
	v.Icon.Level = string(usage.LevelNone)
	if !ok || !InUse(ps) {
		v.Title = usage.FormatPercent(nil)
		if items := BuildMenu(in); visibleText(items, "p.claude") != "" && len(SectionsShown(in.Snap)) == 0 {
			tip = append(tip, visibleText(items, "p.claude"))
		}
		v.Tooltip = truncateUTF16(strings.Join(tip, "\n"), tooltipMax)
		return v
	}
	v.Icon.Provider = string(p)
	tip[0] += text.Sep + p.Name()
	r, have := shownRow(ps)
	stale := !ps.LatestFetchedAt.IsZero() && usage.Stale(ps.LatestFetchedAt, in.Now)
	v.Icon.Stale = stale
	if have {
		v.Icon.Percent = r.Percent
		level := r.Level
		switch {
		case r.Percent == nil:
			level = usage.LevelNone
		case level == "":
			level = usage.LevelOf(usage.Limit{Percent: r.Percent, Severity: "normal"})
		}
		v.Icon.Level = string(level)
		pct := usage.FormatPercent(r.Percent)
		if stale {
			pct += text.StaleMark // as next to the icon (Windows shows only the tooltip)
		}
		tip = append(tip, r.Label+" "+pct+blocked(r)+resetPart(r, in.Now))
	}
	v.Title = usage.FormatPercent(v.Icon.Percent)
	if stale {
		v.Title += text.StaleMark
	}
	if pr := problem(p, ps, in.Snap); pr != "" {
		tip = append(tip, pr)
	} else if !ps.LatestFetchedAt.IsZero() {
		tip = append(tip, usage.FormatUpdated(ps.LatestFetchedAt, in.Now))
	}
	v.Tooltip = truncateUTF16(strings.Join(tip, "\n"), tooltipMax)
	return v
}

func visibleText(items []trayproto.Item, id string) string {
	i := slices.IndexFunc(items, func(it trayproto.Item) bool { return it.ID == id && it.Visible })
	if i < 0 {
		return ""
	}
	return items[i].Text
}

// LabelGuide is the widest text next to the icon, for Linux tray hosts
// that reserve its room (XAyatanaLabelGuide).
const LabelGuide = "100%" + text.StaleMark

// ShowsTitle reports whether goos shows a text next to the tray icon: the
// menu bar title on macOS, the label in Ubuntu's top bar (AppIndicator)
// on Linux. Windows shows none.
func ShowsTitle(goos string) bool { return goos == "darwin" || goos == "linux" }

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

// tooltipMax is in UTF-16 units (Windows NOTIFYICONDATA szTip holds 128
// with the NUL).
const tooltipMax = 127

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
