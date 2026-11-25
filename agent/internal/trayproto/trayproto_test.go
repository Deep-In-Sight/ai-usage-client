package trayproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
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

// The usage panel rides in the view line and comes back unchanged.
func TestViewCarriesPanel(t *testing.T) {
	yes, no := true, false
	p := &Panel{Serial: 7, NewVersion: "New version available: 0.9.2", Sections: []PanelSection{{
		Provider: "claude", Name: "Claude", Account: "team-a@… · 3 PCs", Updated: "Updated 18:52", Stale: true,
		Rows: []PanelRow{
			{ID: "r.claude.0", Label: "5-hour", Percent: pf(42), PercentText: "42%", Detail: "Resets Fri 21:20 · in 2h 20m", Level: "normal"},
			{ID: "r.claude.1", Label: "More limits on the dashboard", Level: "none", More: true},
		}}}, Prompt: "", Server: "Server connected",
		Actions: []PanelAction{{ID: "autostart", Text: "Start at login", Enabled: true, Visible: true, Checkbox: true, Checked: true}},
		Top:     []PanelTop{{Provider: "claude", Text: "42% ⚠", Level: "normal", Percent: pf(42.4), Stale: true}}}
	msgs := []Msg{
		{V: Version, Type: TypeView, Items: []Item{{ID: "quit", Text: "Quit", Visible: true}}, Panel: p},
		{V: Version, Type: TypeHello, Token: "0123456789abcdef", Role: RoleShell, EnableOnce: true},
		{V: Version, Type: TypeShell, Attached: &yes, Why: "detach"},
		{V: Version, Type: TypeShell, Supported: &no, Why: "unsupported 44.2"},
		{V: Version, Type: TypeShell, Supported: &yes},
		{V: Version, Type: TypeShell, Enabled: true},
	}
	var b bytes.Buffer
	for _, m := range msgs {
		if err := Encode(&b, m); err != nil {
			t.Fatal(err)
		}
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
	// the new fields stay out of the lines that do not use them
	var q bytes.Buffer
	if err := Encode(&q, Msg{V: Version, Type: TypeQuit}); err != nil {
		t.Fatal(err)
	}
	eqs(t, q.String(), `{"v":1,"type":"quit"}`+"\n")
	// the bridge's shell lines, byte for byte (review I1 added supported:true)
	for _, c := range []struct {
		m    Msg
		want string
	}{
		{Msg{V: Version, Type: TypeShell, Supported: &yes}, `{"v":1,"type":"shell","supported":true}`},
		{Msg{V: Version, Type: TypeShell, Supported: &no, Why: "no shell"}, `{"v":1,"type":"shell","why":"no shell","supported":false}`},
		{Msg{V: Version, Type: TypeShell, Attached: &no, Why: "ping"}, `{"v":1,"type":"shell","why":"ping","attached":false}`},
		{Msg{V: Version, Type: TypeShell, Enabled: true}, `{"v":1,"type":"shell","enabled":true}`},
	} {
		var b bytes.Buffer
		if err := Encode(&b, c.m); err != nil {
			t.Fatal(err)
		}
		eqs(t, b.String(), c.want+"\n")
	}
	eqs(t, TypeShell, "shell")
	eqs(t, RoleShell, "shell")
	if ExitUnsupported != 78 {
		t.Fatalf("ExitUnsupported = %d", ExitUnsupported)
	}
}

func eqs(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A helper of v0.9.0 (its Msg as it was) reads a line with the new fields
// without an error: the fields are only added (Global Constraint 4).
func TestOldHelperIgnoresNewFields(t *testing.T) {
	type oldMsg struct { // trayproto.Msg of v0.9.0
		V        int         `json:"v"`
		Type     string      `json:"type"`
		Token    string      `json:"token,omitempty"`
		Provider string      `json:"provider,omitempty"`
		UID      uint32      `json:"uid,omitempty"`
		IconDir  string      `json:"icon_dir,omitempty"`
		Standby  bool        `json:"standby,omitempty"`
		Stale    []StaleIcon `json:"stale,omitempty"`
		Window   uint64      `json:"window,omitempty"`
		Icon     *Icon       `json:"icon,omitempty"`
		Title    string      `json:"title,omitempty"`
		Tooltip  string      `json:"tooltip,omitempty"`
		Items    []Item      `json:"items,omitempty"`
		Text     string      `json:"text,omitempty"`
		ID       string      `json:"id,omitempty"`
		Why      string      `json:"why,omitempty"`
	}
	yes := true
	var b bytes.Buffer
	if err := Encode(&b, Msg{V: Version, Type: TypeView, Title: "42%", Role: RoleShell, Attached: &yes,
		Panel: &Panel{Serial: 1, Sections: []PanelSection{}, Actions: []PanelAction{}, Top: []PanelTop{}}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"panel":`, `"role":`, `"attached":`} {
		if !strings.Contains(b.String(), k) {
			t.Fatalf("%s missing in %s", k, b.String())
		}
	}
	var m oldMsg
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeView || m.Title != "42%" {
		t.Fatalf("%+v", m)
	}
}

// The panel of the picture fixture (written by package tray's
// TestPanelJSONNames) stays well inside a line: design 6.5 "under 4 KiB".
func TestPanelFitsALine(t *testing.T) {
	b, err := os.ReadFile("../tray/testdata/panel-combined.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) >= 4096 {
		t.Fatalf("the fixture's panel is %d bytes", len(b))
	}
	var p Panel
	if err := json.Unmarshal(b, &p); err != nil || len(p.Sections) != 2 {
		t.Fatalf("%v %+v", err, p)
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
