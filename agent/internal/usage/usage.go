// Package usage reads the plan usage reply and turns it into display values.
// It is a port of claude-usage@localhost/usage.js and keeps its texts and rules.
package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// ScopedModel is the model whose weekly limit gets the second bar.
const ScopedModel = "Fable"

// StaleAfter is how old shown numbers may get before they are marked stale.
const StaleAfter = 15 * time.Minute

// NotSignedInText is shown when no Claude Code sign-in is found.
const NotSignedInText = "Not signed in: no Claude Code login found"

var days = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// Limit is one usage limit. Percent and ResetsAt are nil when not reported.
type Limit struct {
	Label    string
	Percent  *float64
	ResetsAt *time.Time
	Severity string
}

// Limits holds the 5-hour limit, the all-models weekly limit and the
// weekly limit scoped to a model. Problem is "no_weekly" when a reply (a
// JSON object) had no weekly value in either shape, else "".
type Limits struct {
	FiveHour, Weekly, Scoped Limit
	Problem                  string
}

// ProblemNoWeekly: the reply has no weekly value; it is reported to the
// server as the provider's parse_problem.
const ProblemNoWeekly = "no_weekly"

// ParseTime reads an RFC 3339 timestamp (microseconds allowed), also with
// a space instead of the "T" and a lower-case "z", as the server reads them
// (review m-P). A time without an offset is no time. It returns nil for
// anything else. Like the JS version it keeps milliseconds at most.
func ParseTime(v any) *time.Time {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if len(s) > 10 && s[10] == ' ' {
		s = s[:10] + "T" + s[11:]
	}
	if strings.HasSuffix(s, "z") {
		s = s[:len(s)-1] + "Z"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.Truncate(time.Millisecond)
	return &t
}

func emptyLimit(label string) Limit { return Limit{Label: label, Severity: "normal"} }

// Percents outside this range are no value (the server's rule).
const (
	percentMin = 0
	percentMax = 1000
)

// number is a percent: a finite number from 0 to 1000, else nil.
func number(v any) *float64 {
	if x, ok := v.(float64); ok && !math.IsInf(x, 0) && !math.IsNaN(x) && x >= percentMin && x <= percentMax {
		return &x
	}
	return nil
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// sameWindowPoints: an older bucket's reset time fills a limits[] entry
// without one only when their percentages agree within this many points
// (both describe the same window); the server uses the same rule.
const sameWindowPoints = 1.0

// placeholder: a limits[] entry that is not a window. Ported from
// sirmalloc/ccstatusline (MIT) src/utils/usage-fetch.ts @ 0c94df7:
// isPlaceholderUsageApiLimit (0 % or no percent, and no reset time; a
// named model's entry is real data, 0 until the model is first used).
func placeholder(item map[string]any, model bool) bool {
	if model {
		return false
	}
	pct, _ := item["percent"].(float64)
	return (item["percent"] == nil || pct == 0) && item["resets_at"] == nil
}

// Ported from sirmalloc/ccstatusline (MIT) src/utils/usage-fetch.ts @ 0c94df7:
// findUsageApiLimit, isPlaceholderUsageApiLimit, findWeeklyScopedLimit and
// parseUsageApiResponse: the 5-hour entry is kind "session", a model's
// weekly entry is matched by its display name (case-insensitive, part of
// the name), placeholders are skipped, and each value falls back to the
// older five_hour / seven_day buckets on its own.
// Our rules differ on purpose: limits[] comes first (as in v0.8), a null
// older bucket is no value (not 0), and an older bucket's reset time fills
// an entry only when both describe the same window.
//
// ParseLimits picks the 5-hour limit, the all-models weekly limit and the
// weekly limit scoped to model. It never fails: anything it cannot read is
// "no data".
func ParseLimits(payload json.RawMessage, model string) Limits {
	five := emptyLimit("5-hour")
	weekly := emptyLimit("All models")
	scoped := emptyLimit(model)
	var root any
	if json.Unmarshal(payload, &root) != nil {
		root = nil
	}
	p := object(root)
	found := map[*Limit]bool{}
	times := map[*Limit]*time.Time{} // reset times of entries without a percent
	list, _ := p["limits"].([]any)
	for _, raw := range list {
		item := object(raw)
		if item == nil {
			continue
		}
		scopeModel, _ := object(object(item["scope"])["model"])["display_name"].(string)
		var target *Limit
		switch kind := item["kind"]; {
		case kind == "session" || kind == "five_hour":
			target = &five
		case kind == "weekly_all":
			target = &weekly
		case kind == "weekly_scoped" && model != "" && strings.Contains(strings.ToLower(scopeModel), strings.ToLower(model)):
			target = &scoped
		default:
			continue
		}
		if found[target] || placeholder(item, target == &scoped) {
			continue
		}
		pct := number(item["percent"])
		if pct == nil {
			if at := ParseTime(item["resets_at"]); at != nil && times[target] == nil {
				times[target] = at
			}
			continue
		}
		found[target] = true
		target.Percent = pct
		target.ResetsAt = ParseTime(item["resets_at"])
		switch sev := item["severity"].(type) {
		case nil:
			target.Severity = "normal"
		case string:
			target.Severity = sev
		default:
			target.Severity = fmt.Sprint(sev)
		}
	}
	// the older shape fills what limits[] lacks, value by value
	for _, f := range []struct {
		field  string
		target *Limit
	}{{"seven_day", &weekly}, {"five_hour", &five}} {
		bucket := object(p[f.field])
		pct, at := number(bucket["utilization"]), ParseTime(bucket["resets_at"])
		if f.target == &five && (pct == nil || *pct == 0) && at == nil {
			continue // a placeholder: no 5-hour window
		}
		switch {
		case !found[f.target] && pct != nil:
			f.target.Percent = pct
			f.target.ResetsAt = at
			if at == nil {
				f.target.ResetsAt = times[f.target]
			}
		case found[f.target] && f.target.ResetsAt == nil && at != nil && pct != nil &&
			math.Abs(*pct-*f.target.Percent) <= sameWindowPoints:
			f.target.ResetsAt = at
		}
	}
	l := Limits{FiveHour: five, Weekly: weekly, Scoped: scoped}
	if p != nil && weekly.Percent == nil {
		l.Problem = ProblemNoWeekly
	}
	return l
}

// Level is the colour step of a limit.
type Level string

const (
	LevelNone     Level = "none"
	LevelNormal   Level = "normal"
	LevelWarning  Level = "warning"
	LevelCritical Level = "critical"
)

// LevelOf gives the colour step of a limit.
func LevelOf(l Limit) Level {
	if l.Percent == nil {
		return LevelNone
	}
	p := *l.Percent
	if p >= 90 {
		return LevelCritical
	}
	// The API flags a limit (e.g. "warning") when it is being used up fast,
	// which can be well below 75%.
	if p >= 75 || l.Severity != "normal" {
		return LevelWarning
	}
	return LevelNormal
}

// FormatPercent rounds like JS Math.round.
func FormatPercent(p *float64) string {
	if p == nil {
		return "–%"
	}
	r := math.Floor(*p + 0.5)
	if r == 0 {
		r = 0 // no "-0%"
	}
	return fmt.Sprintf("%.0f%%", r)
}

// BarFraction is the fraction of the bar to fill, 0..1.
func BarFraction(p *float64) float64 {
	if p == nil {
		return 0
	}
	return math.Min(math.Max(*p, 0), 100) / 100
}

// FormatClock is HH:MM in t's own location.
func FormatClock(t time.Time) string { return t.Format("15:04") }

// FormatUpdated says when the shown numbers were fetched; it names the day
// once that is not today (in now's location).
func FormatUpdated(t, now time.Time) string {
	t = t.In(now.Location())
	ty, tm, td := t.Date()
	ny, nm, nd := now.Date()
	if ty == ny && tm == nm && td == nd {
		return "Updated " + FormatClock(t)
	}
	return "Updated " + days[t.Weekday()] + " " + FormatClock(t)
}

// FormatReset describes when a limit resets, relative to now.
func FormatReset(resetsAt *time.Time, now time.Time) string {
	if resetsAt == nil {
		return "No reset time reported"
	}
	// The API's reset timestamps jitter around the minute, so round to the
	// nearest minute.
	reset := resetsAt.Round(time.Minute).In(now.Location())
	when := days[reset.Weekday()] + " " + FormatClock(reset)
	minutes := int(math.Floor(float64(reset.Sub(now)) / float64(time.Minute)))
	if minutes <= 0 {
		return "Reset due " + when
	}
	d, h, m := minutes/1440, (minutes%1440)/60, minutes%60
	var left string
	switch {
	case d > 0:
		left = fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		left = fmt.Sprintf("%dh %dm", h, m)
	default:
		left = fmt.Sprintf("%dm", m)
	}
	return "Resets " + when + " · in " + left
}

// Stale reports whether numbers fetched at fetchedAt are too old to trust.
func Stale(fetchedAt, now time.Time) bool { return now.Sub(fetchedAt) > StaleAfter }

// FetchErrorText is the message for a failed fetch ("" for 200).
func FetchErrorText(httpStatus int) string {
	switch httpStatus {
	case 200:
		return ""
	case 401:
		return "Sign-in expired: open Claude Code to renew it"
	case 429:
		return "Rate limited by Anthropic, retrying later"
	case 0:
		return "Offline: can't reach api.anthropic.com"
	}
	return fmt.Sprintf("Usage request failed (HTTP %d)", httpStatus)
}
