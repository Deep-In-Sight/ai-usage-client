package usage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Trimmed copy of a real reply from the usage endpoint (same as tests/test_usage.js).
const sample = `{"seven_day":{"utilization":71.0,"resets_at":"2026-11-21T16:00:00.113116+00:00"},
 "limits":[{"kind":"weekly_all","group":"weekly","percent":71,"severity":"normal","resets_at":"2026-11-21T16:00:00.113116+00:00","scope":null},
 {"kind":"weekly_scoped","group":"weekly","percent":76,"severity":"warning","resets_at":"2026-11-21T16:00:00.113280+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}}]}`

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func f(x float64) *float64 { return &x }

func TestParseSample(t *testing.T) {
	l := ParseLimits(json.RawMessage(sample), ScopedModel)
	eq(t, *l.Weekly.Percent, 71.0)
	eq(t, LevelOf(l.Weekly), LevelNormal)
	eq(t, *l.Scoped.Percent, 76.0)
	eq(t, l.Scoped.Label, "Fable")
	eq(t, LevelOf(l.Scoped), LevelWarning)
	eq(t, l.Scoped.ResetsAt.UTC().Format("2006-01-02T15:04:05.000Z"), "2026-11-21T16:00:00.113Z")
	eq(t, l.Weekly.Label, "All models")
}

func TestOtherModelDoesNotFillScoped(t *testing.T) {
	p := `{"limits":[{"kind":"weekly_scoped","group":"weekly","percent":76,"severity":"warning",
	 "resets_at":"2026-11-21T16:00:00.113280+00:00","scope":{"model":{"display_name":"Opus"}}}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, l.Scoped.Percent == nil, true)
	eq(t, LevelOf(l.Scoped), LevelNone)
}

func TestScopedModelMatchIgnoresCase(t *testing.T) {
	p := `{"limits":[{"kind":"weekly_scoped","percent":12,"scope":{"model":{"display_name":"fABLE"}}}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, *l.Scoped.Percent, 12.0)
	eq(t, l.Scoped.Severity, "normal")
}

func TestFallsBackToSevenDay(t *testing.T) {
	p := `{"seven_day":{"utilization":71.0,"resets_at":"2026-11-21T16:00:00.113116+00:00"},"limits":null}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, *l.Weekly.Percent, 71.0)
	eq(t, l.Weekly.ResetsAt != nil, true)
	eq(t, l.Scoped.Percent == nil, true)
}

func TestEmptyAndNullPayloads(t *testing.T) {
	for _, p := range []string{`{}`, `null`, ``, `[1,2]`, `{"limits":{"kind":"weekly_all"}}`, `not json`} {
		l := ParseLimits(json.RawMessage(p), ScopedModel)
		if l.Weekly.Percent != nil {
			t.Fatalf("%q: weekly %v", p, *l.Weekly.Percent)
		}
	}
	eq(t, FormatPercent(nil), "–%")
}

func TestNullEntriesAndScopes(t *testing.T) {
	p := `{"limits":[null,{"kind":"weekly_scoped","scope":null},{"kind":"weekly_scoped","scope":{"model":{"display_name":null}}}]}`
	eq(t, ParseLimits(json.RawMessage(p), ScopedModel).Scoped.Percent == nil, true)
}

func TestPercentAsStringIsNoData(t *testing.T) {
	p := `{"limits":[{"kind":"weekly_all","percent":"71","severity":"normal"}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, l.Weekly.Percent == nil, true)
}

func TestParseTimeBad(t *testing.T) {
	eq(t, ParseTime("not a date") == nil, true)
	eq(t, ParseTime(nil) == nil, true)
	eq(t, ParseTime(12.0) == nil, true)
	got := ParseTime("2026-11-21T16:00:00.113116+00:00")
	eq(t, got.UTC().Format(time.RFC3339Nano), "2026-11-21T16:00:00.113Z")
}

func TestLevels(t *testing.T) {
	at := func(p float64, sev string) Level { return LevelOf(Limit{Percent: f(p), Severity: sev}) }
	eq(t, at(10, "normal"), LevelNormal)
	eq(t, at(75, "normal"), LevelWarning)
	eq(t, at(40, "warning"), LevelWarning)
	eq(t, at(90, "normal"), LevelCritical)
	eq(t, at(130, "normal"), LevelCritical)
	eq(t, LevelOf(Limit{Severity: "normal"}), LevelNone)
}

func TestBarFraction(t *testing.T) {
	eq(t, BarFraction(nil), 0.0)
	eq(t, BarFraction(f(50)), 0.5)
	eq(t, BarFraction(f(250)), 1.0)
	eq(t, BarFraction(f(-5)), 0.0)
}

func TestFormatPercent(t *testing.T) {
	eq(t, FormatPercent(f(71.4)), "71%")
	eq(t, FormatPercent(f(71.5)), "72%")
	eq(t, FormatPercent(f(-0.4)), "0%")
}

func TestFormatResetAndUpdated(t *testing.T) {
	loc := time.FixedZone("UTC+9", 9*3600)           // any zone: texts follow now.Location()
	now := time.Date(2026, 11, 20, 19, 0, 0, 0, loc) // Fri 19:00
	plus := func(m int) *time.Time { x := now.Add(time.Duration(m) * time.Minute); return &x }
	eq(t, FormatReset(plus(30*60), now), "Resets Sun 01:00 · in 1d 6h")
	eq(t, FormatReset(plus(5*60+12), now), "Resets Sat 00:12 · in 5h 12m")
	eq(t, FormatReset(plus(7), now), "Resets Fri 19:07 · in 7m")
	j := plus(30 * 60).Add(-100 * time.Millisecond)
	eq(t, FormatReset(&j, now), "Resets Sun 01:00 · in 1d 6h")
	eq(t, FormatReset(nil, now), "No reset time reported")
	if !strings.HasPrefix(FormatReset(plus(-3), now), "Reset due") {
		t.Fatal("past reset")
	}
	eq(t, FormatUpdated(*plus(-40), now), "Updated 18:20")
	eq(t, FormatUpdated(*plus(-20 * 60), now), "Updated Thu 23:00")
	// a UTC timestamp is shown in now's zone
	eq(t, FormatUpdated(plus(-40).UTC(), now), "Updated 18:20")
	eq(t, FormatClock(time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)), "03:04")
}

func TestStale(t *testing.T) {
	now := time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC)
	eq(t, Stale(now.Add(-(14*time.Minute+59*time.Second)), now), false)
	eq(t, Stale(now.Add(-(15*time.Minute+time.Second)), now), true)
}

func TestFetchErrorText(t *testing.T) {
	eq(t, FetchErrorText(401), "Sign-in expired: open Claude Code to renew it")
	eq(t, FetchErrorText(0), "Offline: can't reach api.anthropic.com")
	eq(t, FetchErrorText(503), "Usage request failed (HTTP 503)")
	eq(t, FetchErrorText(429), "Rate limited by Anthropic, retrying later")
	eq(t, FetchErrorText(200), "")
	eq(t, NotSignedInText, "Not signed in: no Claude Code login found")
}
