package trayproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func pf(v float64) *float64 { return &v }

func TestRoundTrip(t *testing.T) {
	msgs := []Msg{
		{V: Version, Type: TypeHello, Token: "0123456789abcdef"},
		{V: Version, Type: TypeView, Icon: &Icon{Provider: "claude", Percent: pf(42.5), Level: "normal", Stale: true},
			Title: "43% ⚠", Tooltip: "AI Usage · Claude\nWeekly 43%", Items: []Item{
				{ID: "h.claude", Text: "Claude · team-a@…", Image: "claude", Visible: true},
				{ID: "autostart", Text: "Start at login", Enabled: true, Visible: true, Checkbox: true, Checked: true},
			}},
		{V: Version, Type: TypeNotice, Text: "AI Usage is running."},
		{V: Version, Type: TypeQuit},
		{V: Version, Type: TypeReady},
		{V: Version, Type: TypeWaiting, Why: "no StatusNotifierWatcher"},
		{V: Version, Type: TypeClick, ID: "refresh"},
	}
	var b bytes.Buffer
	for _, m := range msgs {
		if err := Encode(&b, m); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(b.String(), "\n"); n != len(msgs) {
		t.Fatalf("%d lines for %d messages", n, len(msgs))
	}
	r := bufio.NewReader(&b)
	for i, want := range msgs {
		got, err := Decode(r)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("message %d: got %+v, want %+v", i, got, want)
		}
	}
	if _, err := Decode(r); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last line: %v", err)
	}
}

// A view's items are sent with every field, so that a hidden or disabled
// item is not mistaken for a missing one.
func TestItemFieldsAlwaysSent(t *testing.T) {
	var b bytes.Buffer
	if err := Encode(&b, Msg{V: Version, Type: TypeView, Items: []Item{{ID: "quit"}}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"enabled":false`, `"visible":false`, `"checkbox":false`, `"checked":false`, `"text":""`, `"image":""`} {
		if !strings.Contains(b.String(), k) {
			t.Fatalf("%s missing in %s", k, b.String())
		}
	}
}

func TestLineLimit(t *testing.T) {
	long := strings.Repeat("x", MaxLine)
	var b bytes.Buffer
	if err := Encode(&b, Msg{V: Version, Type: TypeNotice, Text: long}); !errors.Is(err, ErrTooLong) {
		t.Fatalf("Encode of a line over %d bytes: %v", MaxLine, err)
	}
	if b.Len() != 0 {
		t.Fatal("a refused message was written")
	}
	// A line just under the limit passes; one over it is refused.
	ok := `{"v":1,"type":"notice","text":"` + strings.Repeat("y", MaxLine-40) + `"}` + "\n"
	if len(ok) > MaxLine {
		t.Fatal("test line too long")
	}
	if _, err := Decode(bufio.NewReader(strings.NewReader(ok))); err != nil {
		t.Fatalf("line of %d bytes: %v", len(ok), err)
	}
	over := `{"v":1,"type":"notice","text":"` + long + `"}` + "\n"
	if _, err := Decode(bufio.NewReaderSize(strings.NewReader(over), 16)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("line of %d bytes: %v", len(over), err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, s := range []string{"not json\n", "[1,2]\n", `{"v":1}` + "\n", "\n"} {
		if _, err := Decode(bufio.NewReader(strings.NewReader(s))); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("%q: %v", s, err)
		}
	}
	// a last line without its newline still counts
	m, err := Decode(bufio.NewReader(strings.NewReader(`{"v":1,"type":"quit"}`)))
	if err != nil || m.Type != TypeQuit {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestItemIDs(t *testing.T) {
	want := []string{"newversion",
		"h.claude", "r.claude.0", "r.claude.1", "r.claude.2", "r.claude.3", "r.claude.4", "p.claude",
		"h.codex", "r.codex.0", "r.codex.1", "r.codex.2", "r.codex.3", "r.codex.4", "p.codex",
		"server", "dashboard", "refresh", "autostart", "remove", "quit"}
	if !reflect.DeepEqual(ItemIDs, want) {
		t.Fatalf("%v", ItemIDs)
	}
	for id, sec := range map[string]string{"h.claude": "claude", "r.codex.3": "codex", "p.codex": "codex", "server": "", "newversion": "", "remove": ""} {
		if got := Section(id); got != sec {
			t.Fatalf("Section(%q) = %q, want %q", id, got, sec)
		}
	}
	if got := Sections(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("Sections() = %v", got)
	}
}
