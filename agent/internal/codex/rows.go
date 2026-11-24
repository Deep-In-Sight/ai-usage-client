package codex

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

// mainFamily is the limit family of Codex itself; others are per model.
const mainFamily = "codex"

// ParseProblem of a reply with no window the agent can read.
const ProblemNoWindows = "no_windows"

// windowName is a window's key part (the server's: 5h, weekly, 30d, 1d,
// else <minutes>m) and its menu label, by its length (design D6).
//
// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/Providers/Codex/CodexAdditionalRateLimitMapper.swift
// @ a26ca57: windows are told apart by their length, never by
// primary/secondary: 6 hours or less is the 5-hour window, 6 days or more
// the weekly one; docs/codex.md: 2,592,000 s is the 30-day window. Weekly
// gets an upper bound of 8 days here, as on the server, because the
// 30-day window is told apart.
func windowName(mins int) (key, label string) {
	switch {
	case mins <= 360:
		return "5h", "5-hour"
	case mins >= 6*1440 && mins <= 8*1440:
		return "weekly", "Weekly"
	case abs(mins-30*1440) <= 1440:
		return "30d", "30-day"
	case mins == 1440:
		return "1d", "Daily"
	}
	key = strconv.Itoa(mins) + "m"
	switch {
	case mins%60 == 0 && mins < 2880:
		return key, strconv.Itoa(mins/60) + "h"
	case mins%1440 == 0:
		return key, strconv.Itoa(mins/1440) + "d"
	}
	return key, strconv.Itoa(mins) + " min"
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

// familyPrefix is a family's key prefix: "codex", or "codex.<slug>" (the
// server's family_slug); "" when the name has no letters or digits.
func familyPrefix(family string) string {
	if family == mainFamily {
		return mainFamily
	}
	slug := strings.Trim(slugRun.ReplaceAllString(strings.ToLower(family), "-"), "-")
	if slug == "" {
		return ""
	}
	return mainFamily + "." + slug
}

// window is one window of a reply, read.
type window struct {
	pct    float64
	mins   int
	resets *time.Time
}

func number(v any) (float64, bool) {
	x, ok := v.(float64)
	return x, ok && !math.IsInf(x, 0) && !math.IsNaN(x)
}

// readWindow reads one window object: its percent (0..1000), its length
// (in units per minute) and its reset time (unix seconds).
func readWindow(v any, pctF, lenF, resetF string, perMinute float64) (window, bool) {
	m, _ := v.(map[string]any)
	if m == nil {
		return window{}, false
	}
	pct, ok := number(m[pctF])
	if !ok || pct < 0 || pct > 1000 {
		return window{}, false
	}
	l, ok := number(m[lenF])
	if !ok || l <= 0 {
		return window{}, false
	}
	mins := int(math.Floor(l / perMinute))
	if mins <= 0 {
		return window{}, false
	}
	w := window{pct: pct, mins: mins}
	if r, ok := number(m[resetF]); ok && r > 0 && r < 1e11 {
		t := time.Unix(int64(r), 0).UTC()
		w.resets = &t
	}
	return w, true
}

// family is one limit family's windows.
type family struct {
	prefix, name string
	windows      []window
	blocked      bool
}

// Parse reads a Codex reply (the direct wham/usage reply or, from the
// fallback, the app-server's account/rateLimits/read result) into rows
// (design D6): the main family's windows shortest first, then the other
// families (a model's limits) as "<name> · <window>". The main limit (D7)
// is the main family's longest window of a day or more, else its longest.
// A blocked family (limit reached) marks its windows at 100 % or more
// blocked, all of them when none is that full (the reply does not say
// which), as the server does.
func Parse(raw json.RawMessage) provider.Parsed {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return provider.Parsed{}
	}
	top, _ := v.(map[string]any)
	if top == nil {
		return provider.Parsed{}
	}
	var fams []family
	if _, ok := top["rateLimitsByLimitId"]; ok {
		fams = appServerFamilies(top)
	} else if _, ok := top["rateLimits"]; ok {
		fams = appServerFamilies(top)
	} else {
		fams = whamFamilies(top)
	}
	var out provider.Parsed
	for _, f := range fams {
		slices.SortStableFunc(f.windows, func(a, b window) int { return a.mins - b.mins })
		seen := map[string]bool{}
		full := false
		for _, w := range f.windows {
			full = full || w.pct >= 100
		}
		for _, w := range f.windows {
			k, label := windowName(w.mins)
			key := f.prefix + ":" + k
			if seen[key] {
				continue
			}
			seen[key] = true
			if f.prefix != mainFamily {
				label = f.name + " · " + label
			}
			pct := w.pct
			r := provider.Row{Key: key, Label: label, Percent: &pct, ResetsAt: w.resets, WindowMins: w.mins,
				Level: usage.LevelOf(usage.Limit{Percent: &pct, Severity: "normal"})}
			if f.blocked && (!full || w.pct >= 100) {
				r.Blocked, r.Level = true, usage.LevelCritical
			}
			out.Rows = append(out.Rows, r)
			if f.prefix == mainFamily {
				out.MainKey = key // shortest first: the last is the longest (a day or more when there is one)
			}
		}
	}
	if len(out.Rows) == 0 {
		out.ParseProblem = ProblemNoWindows
	}
	return out
}

// whamFamilies: rate_limit is the main family, additional_rate_limits[]
// the others (named by limit_name).
func whamFamilies(top map[string]any) []family {
	read := func(rl any, prefix, name string, reached any) (family, bool) {
		m, _ := rl.(map[string]any)
		if m == nil || prefix == "" {
			return family{}, false
		}
		f := family{prefix: prefix, name: name, blocked: m["limit_reached"] == true || reached != nil}
		for _, k := range []string{"primary_window", "secondary_window"} {
			if w, ok := readWindow(m[k], "used_percent", "limit_window_seconds", "reset_at", 60); ok {
				f.windows = append(f.windows, w)
			}
		}
		return f, true
	}
	var out []family
	if f, ok := read(top["rate_limit"], mainFamily, "", top["rate_limit_reached_type"]); ok {
		out = append(out, f)
	}
	extra, _ := top["additional_rate_limits"].([]any)
	for _, item := range extra {
		m, _ := item.(map[string]any)
		name, _ := m["limit_name"].(string)
		if p := familyPrefix(name); p != "" && p != mainFamily {
			if f, ok := read(m["rate_limit"], p, name, nil); ok {
				out = append(out, f)
			}
		}
	}
	return out
}

// appServerFamilies: rateLimitsByLimitId (else the one rateLimits); a
// family other than "codex" is named by its limitName when it has one,
// so that its keys match the direct reply's, else by its id. Codex first,
// the others by id.
func appServerFamilies(top map[string]any) []family {
	byID, _ := top["rateLimitsByLimitId"].(map[string]any)
	if byID == nil {
		one, _ := top["rateLimits"].(map[string]any)
		if one == nil {
			return nil
		}
		id, _ := one["limitId"].(string)
		if id == "" {
			id = mainFamily
		}
		byID = map[string]any{id: one}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		switch {
		case a == mainFamily:
			return -1
		case b == mainFamily:
			return 1
		}
		return strings.Compare(a, b)
	})
	var out []family
	for _, id := range ids {
		snap, _ := byID[id].(map[string]any)
		if snap == nil {
			continue
		}
		name, _ := snap["limitName"].(string)
		fam := id
		if id != mainFamily && strings.TrimSpace(name) != "" {
			fam = name
		}
		p := familyPrefix(fam)
		if p == "" {
			continue
		}
		if name == "" {
			name = id
		}
		f := family{prefix: p, name: name, blocked: snap["rateLimitReachedType"] != nil}
		for _, k := range []string{"primary", "secondary"} {
			if w, ok := readWindow(snap[k], "usedPercent", "windowDurationMins", "resetsAt", 1); ok {
				f.windows = append(f.windows, w)
			}
		}
		out = append(out, f)
	}
	return out
}
