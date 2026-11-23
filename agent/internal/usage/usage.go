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

// Limits holds the all-models weekly limit and the one scoped to a model.
type Limits struct {
	Weekly, Scoped Limit
}

// ParseTime reads an RFC 3339 timestamp (microseconds allowed). It returns
// nil for anything else. Like the JS version it keeps milliseconds at most.
func ParseTime(v any) *time.Time {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.Truncate(time.Millisecond)
	return &t
}

func emptyLimit(label string) Limit { return Limit{Label: label, Severity: "normal"} }

func number(v any) *float64 {
	if x, ok := v.(float64); ok {
		return &x
	}
	return nil
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// ParseLimits picks the all-models weekly limit and the weekly limit scoped
// to model. It never fails: anything it cannot read is "no data".
func ParseLimits(payload json.RawMessage, model string) Limits {
	weekly := emptyLimit("All models")
	scoped := emptyLimit(model)
	var root any
	if json.Unmarshal(payload, &root) != nil {
		root = nil
	}
	p := object(root)
	list, _ := p["limits"].([]any)
	for _, raw := range list {
		item := object(raw)
		if item == nil {
			continue
		}
		scopeModel, _ := object(object(item["scope"])["model"])["display_name"].(string)
		var target *Limit
		switch {
		case item["kind"] == "weekly_all":
			target = &weekly
		case item["kind"] == "weekly_scoped" && strings.EqualFold(scopeModel, model):
			target = &scoped
		default:
			continue
		}
		target.Percent = number(item["percent"])
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
	if weekly.Percent == nil { // older reply shape without `limits`
		sevenDay := object(p["seven_day"])
		if u := number(sevenDay["utilization"]); u != nil {
			weekly.Percent = u
			weekly.ResetsAt = ParseTime(sevenDay["resets_at"])
		}
	}
	return Limits{Weekly: weekly, Scoped: scoped}
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
	case 0:
		return "Offline: can't reach api.anthropic.com"
	}
	return fmt.Sprintf("Usage request failed (HTTP %d)", httpStatus)
}
