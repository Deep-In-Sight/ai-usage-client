package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

func fixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	must(t, err)
	return b
}

type rowWant struct {
	key, label string
	pct        float64
	mins       int
	blocked    bool
}

func rowsOf(p provider.Parsed) []rowWant {
	var out []rowWant
	for _, r := range p.Rows {
		w := rowWant{key: r.Key, label: r.Label, mins: r.WindowMins, blocked: r.Blocked}
		if r.Percent != nil {
			w.pct = *r.Percent
		}
		out = append(out, w)
	}
	return out
}

// Both reply shapes (the direct wham/usage reply and the app-server's)
// give the same keys and main limit as the server reads (its fixtures,
// copied unchanged into testdata/), in menu order (design D6, D7).
func TestParseShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		rows []rowWant
		main string
	}{
		{"wham-weekly-only", []rowWant{{"codex:weekly", "Weekly", 38, 10080, false}}, "codex:weekly"},
		{"wham-plus", []rowWant{{"codex:5h", "5-hour", 100, 300, true}, {"codex:weekly", "Weekly", 38, 10080, false}}, "codex:weekly"},
		{"wham-free-30d", []rowWant{{"codex:30d", "30-day", 12.5, 43200, false}}, "codex:30d"},
		{"wham-with-additional", []rowWant{{"codex:5h", "5-hour", 20, 300, false}, {"codex:weekly", "Weekly", 45, 10080, false},
			{"codex.gpt-5-3-codex-spark:5h", "GPT-5.3-Codex-Spark · 5-hour", 5, 300, false},
			{"codex.gpt-5-3-codex-spark:weekly", "GPT-5.3-Codex-Spark · Weekly", 9, 10080, false}}, "codex:weekly"},
		{"appserver-pro", []rowWant{{"codex:weekly", "Weekly", 38, 10080, false}}, "codex:weekly"},
		{"wham-unknown", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Parse(fixture(t, c.name))
			eq(t, rowsOf(p), c.rows)
			eq(t, p.MainKey, c.main)
			want := ""
			if c.rows == nil {
				want = "no_windows"
			}
			eq(t, p.ParseProblem, want)
		})
	}
	// reset times and levels
	p := Parse(fixture(t, "wham-plus"))
	eq(t, p.Rows[0].ResetsAt.Equal(time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC)), true)
	eq(t, p.Rows[0].Level, usage.LevelCritical)
	eq(t, p.Rows[1].Level, usage.LevelNormal)
	r, ok := provider.Shown(p) // the blocked 5-hour window drives the icon (D7)
	eq(t, ok, true)
	eq(t, r.Key, "codex:5h")
	// no reply: no rows, no problem
	eq(t, Parse(nil).ParseProblem, "")
	eq(t, len(Parse(json.RawMessage("null")).Rows), 0)
}

// Windows are named by their length, never by primary/secondary (design
// D6, B5); the keys are the server's.
func TestWindowNames(t *testing.T) {
	for mins, want := range map[int][2]string{
		60: {"5h", "5-hour"}, 300: {"5h", "5-hour"}, 360: {"5h", "5-hour"}, 361: {"361m", "361 min"},
		720: {"720m", "12h"}, 1440: {"1d", "Daily"}, 2880: {"2880m", "2d"}, 8640: {"weekly", "Weekly"},
		10080: {"weekly", "Weekly"}, 11520: {"weekly", "Weekly"}, 11521: {"11521m", "11521 min"},
		41760: {"30d", "30-day"}, 43200: {"30d", "30-day"}, 44640: {"30d", "30-day"}, 20160: {"20160m", "14d"},
	} {
		k, l := windowName(mins)
		eq(t, [2]string{k, l}, want)
	}
}

// Odd replies never panic and give no rows; the main limit is the
// longest main window of a day or more, else the longest one (the icon
// and the server's "now" show it; design D7).
func TestParseOddReplies(t *testing.T) {
	for _, raw := range []string{`[]`, `"x"`, `{"rate_limit":"x"}`, `{"rate_limit":{"primary_window":{"used_percent":"50"}}}`,
		`{"rate_limit":{"primary_window":{"used_percent":50}}}`,
		`{"rate_limit":{"primary_window":{"used_percent":5000,"limit_window_seconds":18000}}}`,
		`{"additional_rate_limits":[null,{"limit_name":5},{"rate_limit":[]}]}`,
		`{"rateLimitsByLimitId":{"codex":null,"x":{"primary":{"usedPercent":true,"windowDurationMins":300}}}}`} {
		p := Parse(json.RawMessage(raw))
		if len(p.Rows) != 0 {
			t.Fatalf("%s: %+v", raw, p.Rows)
		}
	}
	p := Parse(json.RawMessage(`{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000},"secondary_window":{"used_percent":3,"limit_window_seconds":43200}}}`))
	eq(t, p.MainKey, "codex:720m")
	eq(t, rowsOf(p)[0].key, "codex:5h")
	// a blocked family whose windows are all below 100 %: all of them are
	p = Parse(json.RawMessage(`{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":7,"limit_window_seconds":18000},"secondary_window":{"used_percent":3,"limit_window_seconds":604800}}}`))
	eq(t, p.Rows[0].Blocked && p.Rows[1].Blocked, true)
	// the app-server's other families are named by limitName, as the
	// direct reply's additional_rate_limits
	p = Parse(json.RawMessage(`{"rateLimitsByLimitId":{"codex":{"primary":{"usedPercent":38,"windowDurationMins":10080}},
		"codex_bengalfox":{"limitName":"GPT-5.3-Codex-Spark","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1767700800},"rateLimitReachedType":"primary"}}}`))
	eq(t, rowsOf(p), []rowWant{{"codex:weekly", "Weekly", 38, 10080, false}, {"codex.gpt-5-3-codex-spark:5h", "GPT-5.3-Codex-Spark · 5-hour", 100, 300, true}})
}
