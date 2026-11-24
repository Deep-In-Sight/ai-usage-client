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

// Design D5 / B6 (ccstatusline's rules): the new shape names the 5-hour
// window "session"; "five_hour" is taken as well.
func TestSessionKindIsFiveHour(t *testing.T) {
	for _, kind := range []string{"session", "five_hour"} {
		p := `{"limits":[{"kind":"` + kind + `","percent":40,"resets_at":"2026-11-24T14:00:00Z"},
		 {"kind":"weekly_all","percent":10,"resets_at":"2026-11-28T14:00:00Z"}]}`
		l := ParseLimits(json.RawMessage(p), ScopedModel)
		eq(t, *l.FiveHour.Percent, 40.0)
		eq(t, l.FiveHour.ResetsAt.UTC(), time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC))
		eq(t, l.FiveHour.Label, "5-hour")
		eq(t, *l.Weekly.Percent, 10.0)
		eq(t, l.Problem, "")
	}
}

// A non-model entry at 0 % (or none) with no reset time is a placeholder,
// not a window: the older field is used.
func TestPlaceholderLimitIgnored(t *testing.T) {
	p := `{"five_hour":{"utilization":12,"resets_at":"2026-11-24T14:00:00Z"},
	 "limits":[{"kind":"session","percent":0,"resets_at":null},{"kind":"weekly_all","resets_at":null},
	 {"kind":"weekly_all","percent":33,"resets_at":"2026-11-28T14:00:00Z"}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, *l.FiveHour.Percent, 12.0)
	eq(t, l.FiveHour.ResetsAt != nil, true)
	eq(t, *l.Weekly.Percent, 33.0) // the placeholder before it did not win
	// a model's entry at 0 % with no reset is real (the model is not used yet)
	m := ParseLimits(json.RawMessage(`{"limits":[{"kind":"weekly_scoped","percent":0,"resets_at":null,"scope":{"model":{"display_name":"Fable"}}}]}`), ScopedModel)
	eq(t, *m.Scoped.Percent, 0.0)
}

// Model names may carry a family prefix: case-insensitive, part of the
// name; the first match wins.
func TestScopedSubstringMatch(t *testing.T) {
	p := `{"limits":[{"kind":"weekly_scoped","percent":25,"scope":{"model":{"display_name":"Claude 3.5 Fable"}}},
	 {"kind":"weekly_scoped","percent":99,"scope":{"model":{"display_name":"Fable"}}}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, *l.Scoped.Percent, 25.0)
	eq(t, l.Scoped.Label, "Fable")
}

// Each field falls back to the older shape on its own.
func TestFieldwiseFallback(t *testing.T) {
	p := `{"five_hour":{"utilization":30,"resets_at":"2026-11-24T14:00:00Z"},
	 "limits":[{"kind":"weekly_all","percent":20,"resets_at":"2026-11-28T14:00:00Z"}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, *l.FiveHour.Percent, 30.0)
	eq(t, *l.Weekly.Percent, 20.0)
	// an entry with a reset time but no percent lends its time to the older value
	q := `{"seven_day":{"utilization":25},"limits":[{"kind":"weekly_all","resets_at":"2026-11-28T14:00:00Z"}]}`
	w := ParseLimits(json.RawMessage(q), ScopedModel).Weekly
	eq(t, *w.Percent, 25.0)
	eq(t, w.ResetsAt.UTC(), time.Date(2026, 11, 28, 14, 0, 0, 0, time.UTC))
	// an entry's missing reset time is taken from the older bucket only when
	// both describe the same window (percentages within 1 point)
	same := `{"seven_day":{"utilization":20.5,"resets_at":"2026-11-28T14:00:00Z"},"limits":[{"kind":"weekly_all","percent":20}]}`
	eq(t, ParseLimits(json.RawMessage(same), ScopedModel).Weekly.ResetsAt != nil, true)
	other := `{"seven_day":{"utilization":40,"resets_at":"2026-11-28T14:00:00Z"},"limits":[{"kind":"weekly_all","percent":20}]}`
	ow := ParseLimits(json.RawMessage(other), ScopedModel).Weekly
	eq(t, *ow.Percent, 20.0)
	eq(t, ow.ResetsAt == nil, true)
}

// A null older bucket is "no value", not 0 (the menu hides absent rows);
// so is an older 5-hour bucket at 0 % with no reset time.
func TestNullOldBucketIsNoValue(t *testing.T) {
	for _, p := range []string{`{"five_hour":null,"seven_day":{"utilization":5}}`, `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":5}}`} {
		l := ParseLimits(json.RawMessage(p), ScopedModel)
		eq(t, l.FiveHour.Percent == nil, true)
		eq(t, *l.Weekly.Percent, 5.0)
	}
}

// A JSON object without any weekly value is a reply the agent could not
// read: Problem "no_weekly". Anything that is not an object is no reply.
func TestNoWeeklyProblem(t *testing.T) {
	eq(t, ParseLimits(json.RawMessage(`{"limits":[]}`), ScopedModel).Problem, "no_weekly")
	eq(t, ParseLimits(json.RawMessage(`{}`), ScopedModel).Problem, "no_weekly")
	eq(t, ParseLimits(json.RawMessage(`{"five_hour":{"utilization":3}}`), ScopedModel).Problem, "no_weekly")
	for _, p := range []string{`null`, ``, `[1,2]`, `not json`} {
		eq(t, ParseLimits(json.RawMessage(p), ScopedModel).Problem, "")
	}
	eq(t, ParseLimits(json.RawMessage(sample), ScopedModel).Problem, "")
}

// The reply shape this host's account gives (stage 0, fake numbers): the
// older five_hour is null, both shapes carry the weekly value.
func TestThisHostShape(t *testing.T) {
	p := `{"five_hour":null,"seven_day":{"utilization":30.0,"resets_at":"2026-11-28T16:00:00.000000+00:00"},
	 "seven_day_opus":null,"extra_usage":{"is_enabled":false},
	 "limits":[{"kind":"weekly_all","group":"weekly","percent":30,"severity":"normal","resets_at":"2026-11-28T16:00:00.000000+00:00","scope":null,"is_active":true},
	 {"kind":"weekly_scoped","group":"weekly","percent":25,"severity":"normal","resets_at":"2026-11-28T16:00:00.000000+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}]}`
	l := ParseLimits(json.RawMessage(p), ScopedModel)
	eq(t, *l.Weekly.Percent, 30.0)
	eq(t, *l.Scoped.Percent, 25.0)
	eq(t, l.FiveHour.Percent == nil, true)
	eq(t, l.Problem, "")
}

// The server's rules (app/readings.py, review m-P): a percent is a finite
// number from 0 to 1000, else no value (and the older shape's value is
// used); a reset time may have a space instead of the "T".
func TestPercentOutOfRangeIsNoValue(t *testing.T) {
	for _, bad := range []string{"-5", "5000", "1000.5"} {
		l := ParseLimits([]byte(`{"limits":[{"kind":"weekly_all","percent":`+bad+`}],"seven_day":{"utilization":20}}`), ScopedModel)
		if l.Weekly.Percent == nil || *l.Weekly.Percent != 20 {
			t.Fatalf("percent %s: weekly %v", bad, l.Weekly.Percent)
		}
	}
	l := ParseLimits([]byte(`{"seven_day":{"utilization":1001}}`), ScopedModel)
	if l.Weekly.Percent != nil || l.Problem != ProblemNoWeekly {
		t.Fatalf("1001 alone: %v %q", l.Weekly.Percent, l.Problem)
	}
	l = ParseLimits([]byte(`{"seven_day":{"utilization":1000}}`), ScopedModel)
	if l.Weekly.Percent == nil || *l.Weekly.Percent != 1000 {
		t.Fatal("1000 is a value")
	}
}

func TestResetTimeWithASpace(t *testing.T) {
	want := time.Date(2026, 11, 28, 16, 0, 0, 0, time.UTC)
	for _, s := range []string{"2026-11-28 16:00:00+00:00", "2026-11-28T16:00:00Z", "2026-11-28 16:00:00.000z", "2026-11-29 01:00:00+09:00"} {
		got := ParseTime(s)
		if got == nil || !got.Equal(want) {
			t.Fatalf("%q: %v", s, got)
		}
	}
	for _, s := range []string{"2026-11-28 16:00:00", "2026-11-28  16:00:00Z", "28.11.2026"} {
		if got := ParseTime(s); got != nil {
			t.Fatalf("%q: %v", s, got)
		}
	}
}
