package provider

import (
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

func pf(v float64) *float64 { return &v }

// Design D7: the icon shows the main limit, unless another limit is used
// up (100 % or more): that one is what keeps the user from working.
func TestShownPrefersExhaustedRow(t *testing.T) {
	weekly := Row{Key: "weekly_all", Label: "Weekly", Percent: pf(30), Level: usage.LevelNormal}
	five := Row{Key: "five_hour", Label: "5-hour", Percent: pf(100), Level: usage.LevelCritical}
	p := Parsed{Rows: []Row{five, weekly}, MainKey: "weekly_all"}
	if r, ok := Shown(p); !ok || r.Key != "five_hour" {
		t.Fatalf("got %+v %v", r, ok)
	}
	five.Percent = pf(99)
	p.Rows = []Row{five, weekly}
	if r, ok := Shown(p); !ok || r.Key != "weekly_all" {
		t.Fatalf("got %+v %v", r, ok)
	}
	if _, ok := Shown(Parsed{MainKey: "weekly_all"}); ok {
		t.Fatal("no rows, but something is shown")
	}
	// the main limit itself used up: it stays the shown one
	weekly.Percent = pf(100)
	five.Percent = pf(120)
	if r, _ := Shown(Parsed{Rows: []Row{five, weekly}, MainKey: "weekly_all"}); r.Key != "weekly_all" {
		t.Fatalf("got %+v", r)
	}
	// no main limit named: the first row
	if r, ok := Shown(Parsed{Rows: []Row{{Key: "codex:1d", Percent: pf(5)}}}); !ok || r.Key != "codex:1d" {
		t.Fatalf("got %+v %v", r, ok)
	}
}

func TestRowLookup(t *testing.T) {
	p := Parsed{Rows: []Row{{Key: "a"}, {Key: "b", Label: "B"}}}
	if r, ok := p.Row("b"); !ok || r.Label != "B" {
		t.Fatal(r, ok)
	}
	if _, ok := p.Row("c"); ok {
		t.Fatal("found c")
	}
}
