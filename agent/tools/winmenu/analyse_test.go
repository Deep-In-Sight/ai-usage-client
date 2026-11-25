package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// The CI build's menu at 100 %: header, three rows, separator, header,
// two rows, separator, server line, five actions.
func menuAt(m panel.Metrics) []item {
	var items []item
	y := int32(3)
	add := func(h int, disabled, sep bool) {
		items = append(items, item{Left: 3, Top: y, Right: 380, Bottom: y + int32(h), Owner: true, Disabled: disabled, Sep: sep})
		y += int32(h)
	}
	add(m.Header, true, false)
	for range 3 {
		add(m.Row, false, false)
	}
	add(m.Separator, false, true)
	add(m.Header, true, false)
	add(m.Row, false, false)
	add(m.Row, false, false)
	add(m.Separator, false, true)
	add(m.Item, true, false)
	for range 5 {
		add(m.Item, false, false)
	}
	return items
}

func TestRoles(t *testing.T) {
	items := menuAt(panel.MetricsAt(96))
	h, r, q, err := roles(items)
	if err != nil || h != 0 || r != 1 || q != len(items)-1 {
		t.Fatalf("%d %d %d %v", h, r, q, err)
	}
	// a new-version line first: the header is the first disabled item
	items = append([]item{{Owner: true, Bottom: 30}}, items...)
	if h, r, _, _ = roles(items); h != 1 || r != 2 {
		t.Fatalf("%d %d", h, r)
	}
	if _, _, _, err = roles(items[:3]); err == nil {
		t.Fatal("three items have roles")
	}
}

func TestHeightProblems(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		m := panel.MetricsAt(dpi)
		if p := heightProblems(menuAt(m), m); len(p) != 0 {
			t.Fatalf("dpi %d: %v", dpi, p)
		}
		// measured at another DPI
		if p := heightProblems(menuAt(panel.MetricsAt(96)), panel.MetricsAt(144)); len(p) == 0 {
			t.Fatal("96 DPI sizes pass at 144")
		}
	}
	items := menuAt(panel.MetricsAt(96))
	items[3].Owner = false
	items[1].Bottom++
	got := strings.Join(heightProblems(items, panel.MetricsAt(96)), "; ")
	if !strings.Contains(got, "item 3 not owner-drawn") || !strings.Contains(got, "item 1 is 65 px high") || !strings.Contains(got, "row 65 px, want 64") {
		t.Fatal(got)
	}
}

func TestColours(t *testing.T) {
	if s := (rgb{0x2b, 0x2b, 0x2b}).String(); s != "#2b2b2b" {
		t.Fatal(s)
	}
	if d := diff(rgb{10, 200, 30}, rgb{12, 190, 30}); d != 10 {
		t.Fatal(d)
	}
	if !reflect.DeepEqual([2]bool{lum(rgb{0x2b, 0x2b, 0x2b}) < 0.3, lum(rgb{0xf0, 0xf0, 0xf0}) > 0.7}, [2]bool{true, true}) {
		t.Fatal("luminance")
	}
}
