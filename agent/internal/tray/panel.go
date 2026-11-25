package tray

import (
	"math"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// BuildPanel is the usage panel (drawn-panel design 6.1) for the same
// inputs as BuildMenu, with the same sections, rows, ids, problem lines
// and actions (both take them from the same functions); every text is
// finished here. Its Serial is 0: the tray loop numbers the panels it
// sends (plan PR-6).
func BuildPanel(in MenuInputs) trayproto.Panel {
	s := in.Snap
	p := trayproto.Panel{Sections: []trayproto.PanelSection{}}
	if line, newer := newVersion(in); newer {
		p.NewVersion = line
	}
	ids := SectionsShown(s)
	for _, id := range ids {
		ps := s.Providers[id]
		sec := trayproto.PanelSection{Provider: string(id), Name: id.Name(),
			Account: strings.Join(account(id, ps), text.Sep), Problem: problem(id, ps, s),
			Stale: stale(ps, in.Now), Rows: []trayproto.PanelRow{}}
		if !ps.LatestFetchedAt.IsZero() {
			sec.Updated = usage.FormatUpdated(ps.LatestFetchedAt, in.Now) // no stale mark: the group is faded
		}
		for _, r := range sectionRows(id, ps) {
			if r.more {
				sec.Rows = append(sec.Rows, trayproto.PanelRow{ID: r.id, Label: text.MoreLimits, Level: string(usage.LevelNone), More: true})
				continue
			}
			sec.Rows = append(sec.Rows, trayproto.PanelRow{ID: r.id, Label: r.row.Label, Percent: rounded(r.row.Percent),
				PercentText: usage.FormatPercent(r.row.Percent), Detail: resetDetail(r.row, in.Now), Level: string(rowLevel(r.row))})
		}
		p.Sections = append(p.Sections, sec)
	}
	p.Prompt = prompt(s, ids)
	p.Server = serverLine(in, ids, false)
	for _, a := range actions(in) {
		p.Actions = append(p.Actions, trayproto.PanelAction{ID: a.ID, Text: a.Text, Enabled: a.Enabled, Visible: a.Visible,
			Checkbox: a.Checkbox, Checked: a.Checked})
	}
	p.Top = top(in)
	return p
}

// top is one number per provider in use, in menu order, each its icon's
// (plan PR-5); the neutral "–%" when none is.
func top(in MenuInputs) []trayproto.PanelTop {
	var out []trayproto.PanelTop
	for _, id := range order {
		if ps, ok := in.Snap.Providers[id]; ok && InUse(ps) {
			out = append(out, topOf(BuildIcon(id, in)))
		}
	}
	if len(out) == 0 {
		out = append(out, topOf(BuildIcon("", in)))
	}
	return out
}

func topOf(v supervisor.IconView) trayproto.PanelTop {
	return trayproto.PanelTop{Provider: v.Icon.Provider, Text: v.Title, Level: v.Icon.Level, Percent: v.Icon.Percent, Stale: v.Icon.Stale}
}

// rounded is the percent as shown ("4.5" is 5 %, as FormatPercent): the
// bar's length; nil stays nil.
func rounded(p *float64) *float64 {
	if p == nil {
		return nil
	}
	r := math.Floor(*p + 0.5)
	if r == 0 {
		r = 0 // no -0
	}
	return &r
}

// resetDetail is a row's reset line (plan PR-7, pre-flight F4/F6): "Resets
// Fri 21:20 · in 2h 20m" or "Reset due Fri 18:00", with "Blocked · " in
// front when the limit stops the user; "Blocked" alone, or "" when no reset
// time is reported (the row keeps its height).
func resetDetail(r provider.Row, now time.Time) string {
	if r.ResetsAt == nil || r.ResetsAt.IsZero() {
		if r.Blocked {
			return text.BlockedAlone
		}
		return ""
	}
	when, left, due := usage.ResetParts(*r.ResetsAt, now)
	switch {
	case due && r.Blocked:
		return text.BlockedResetDue + when
	case due:
		return text.ResetDueAt + when
	case r.Blocked:
		return text.BlockedResets + when + text.ResetsIn + left
	}
	return text.ResetsAt + when + text.ResetsIn + left
}
