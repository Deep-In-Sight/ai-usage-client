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
		line := (15*dpi + 48) / 96
		if p := heightProblems(menuAt(m), m, line); len(p) != 0 {
			t.Fatalf("dpi %d: %v", dpi, p)
		}
		// measured at another DPI
		if p := heightProblems(menuAt(panel.MetricsAt(96)), panel.MetricsAt(144), 22); len(p) == 0 {
			t.Fatal("96 DPI sizes pass at 144")
		}
	}
	// the server line on two lines (15 px lines: 30 + 15 + 2), a header
	// with a problem on two lines
	items := menuAt(panel.MetricsAt(96))
	items[9].Bottom += 17
	items[0].Bottom += 28 + 17
	if p := heightProblems(items, panel.MetricsAt(96), 15); len(p) != 0 {
		t.Fatal(p)
	}
	items[9].Bottom++
	if p := heightProblems(items, panel.MetricsAt(96), 15); len(p) != 1 || p[0] != "item 9 is 48 px high" {
		t.Fatal(p)
	}
	items = menuAt(panel.MetricsAt(96))
	items[3].Owner = false
	items[1].Bottom++
	got := strings.Join(heightProblems(items, panel.MetricsAt(96), 15), "; ")
	if !strings.Contains(got, "item 3 not owner-drawn") || !strings.Contains(got, "item 1 is 65 px high") || !strings.Contains(got, "row 65 px, want 64") {
		t.Fatal(got)
	}
}

// The bar's height in a column: the longest warm run, whatever the text
// above and below it.
func TestBarRun(t *testing.T) {
	bg, text, dim := rgb{0xf0, 0xf0, 0xf0}, rgb{0, 0, 0}, rgb{0x6d, 0x6d, 0x6d}
	normal, critical, edge := rgb{0xd9, 0x77, 0x57}, rgb{0xf6, 0x61, 0x51}, rgb{0xec, 0xd5, 0xcc}
	col := []rgb{bg, text, text, text, bg, bg}
	for range 8 {
		col = append(col, normal)
	}
	col = append(col, edge, bg, dim, dim, bg)
	if n := barRun(col); n != 8 {
		t.Fatal(n)
	}
	if n := barRun([]rgb{bg, critical, critical, critical, bg, normal}); n != 3 {
		t.Fatal(n)
	}
	if n := barRun([]rgb{bg, text, dim, bg}); n != 0 {
		t.Fatal(n)
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
