package trayhelper

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// fixtureView is the view the tray loop sends for in: the menu's items
// and the panel.
func fixtureView(in tray.MenuInputs) trayproto.Msg {
	p := tray.BuildPanel(in)
	pct := 42.0
	return trayproto.Msg{Type: trayproto.TypeView, Items: tray.BuildMenu(in), Panel: &p,
		Icon: &trayproto.Icon{Provider: "claude", Percent: &pct, Level: "normal"}, Tooltip: "AI Usage"}
}

func drawnRun(t *testing.T, draws bool, goos string) *run {
	t.Helper()
	ui := newFakeUI()
	ui.draws.Store(draws)
	r := startRun(t, runOpts{goos: goos, ui: ui})
	r.hello()
	return r
}

// Plan Task 8, pre-flight F8: a ui that draws (Windows; macOS from Task 9)
// gets every item of the panel to draw, the header carrying its problem
// line; one that does not (Linux) gets today's menu, unchanged.
func TestPanelItemsAreDrawnOnWindowsAndMac(t *testing.T) {
	v := fixtureView(panelfixture.States())
	want := panel.Items(*v.Panel)

	r := drawnRun(t, true, "windows")
	r.send(v)
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, items, _, _, _, _ := r.ui.snapshot()
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude = "+
		"h.codex r.codex.0 r.codex.1 r.codex.2 r.codex.3 r.codex.4 p.codex = server dashboard refresh autostart remove quit")
	for _, id := range []string{"h.claude", "r.claude.0", "r.claude.1", "r.claude.2", "h.codex", "r.codex.0", "r.codex.1"} {
		if items[id].drawn == nil || !items[id].visible {
			t.Fatalf("%s: drawn %v visible %v", id, items[id].drawn, items[id].visible)
		}
		eq(t, *items[id].drawn, want[id])
	}
	// every visible item is drawn (review B-M10): dashboard, remove and
	// quit too
	for _, id := range layout {
		if id != "=" && items[id].visible && items[id].drawn == nil {
			t.Fatalf("%s is visible and not drawn", id)
		}
	}
	eq(t, items["h.codex"].drawn.Problem, text.CodexExpired)
	eq(t, items["p.codex"].visible, false) // drawn under the header instead
	eq(t, items["p.codex"].drawn == nil, true)
	eq(t, items["h.claude"].image == nil, true) // the header's picture is drawn
	eq(t, items["server"].title, v.Panel.Server)
	eq(t, *items["server"].drawn, panel.Item{Kind: "info", Text: v.Panel.Server, Dim: true})
	eq(t, *items["refresh"].drawn, panel.Item{Kind: "action", Text: "Refresh now", Enabled: true})
	eq(t, *items["autostart"].drawn, panel.Item{Kind: "action", Text: "Start at login", Enabled: true, Checkbox: true, Checked: true})
	eq(t, items["r.claude.3"].visible, false)
	eq(t, items["r.claude.3"].drawn == nil, true)
	eq(t, items["newversion"].drawn == nil, true) // hidden
	if strings.Contains(v.Panel.Server, "Updated") {
		t.Fatalf("server line %q", v.Panel.Server)
	}

	// a ui that does not draw: the text menu of today
	r = drawnRun(t, false, "linux")
	r.send(v)
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, items, _, _, _, _ = r.ui.snapshot()
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude - "+
		"h.codex r.codex.0 r.codex.1 r.codex.2 r.codex.3 r.codex.4 p.codex - server - dashboard refresh autostart remove quit")
	for id := range items {
		if items[id].drawnSets != 0 {
			t.Fatalf("%s drawn on a ui that does not draw", id)
		}
	}
	eq(t, items["p.codex"].visible, true)
	eq(t, items["p.codex"].title, text.CodexExpired)
	if !strings.Contains(items["server"].title, "Updated 18:30") {
		t.Fatalf("server %q", items["server"].title)
	}

	// a ui that draws, without a panel in the view: today's menu too
	r = drawnRun(t, true, "windows")
	nv := v
	nv.Panel = nil
	r.send(nv)
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, items, _, _, _, _ = r.ui.snapshot()
	if strings.Contains(strings.Join(layout, " "), "=") || items["h.codex"].drawnSets != 0 || !items["p.codex"].visible {
		t.Fatalf("drawn without a panel: %v", layout)
	}
}

// Review N6 (stage 3): Windows draws every item and keeps each one's own
// title the text menu's line (its strings stay what they were before the
// drawn panel); only macOS, whose view items VoiceOver reads by their
// titles, gives a drawn item its view's label.
func TestWindowsKeepsTheTextTitlesWhileDrawing(t *testing.T) {
	v := fixtureView(panelfixture.States())
	byID := map[string]trayproto.Item{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	r := drawnRun(t, true, "windows")
	r.send(v)
	eq(t, r.next().Type, trayproto.TypeReady)
	_, items, _, _, _, _ := r.ui.snapshot()
	n := 0
	for id := range panel.Items(*v.Panel) {
		if items[id].drawn == nil {
			continue
		}
		n++
		if items[id].title != byID[id].Text {
			t.Fatalf("%s: title %q, want the text menu's %q", id, items[id].title, byID[id].Text)
		}
	}
	if n < 7 {
		t.Fatalf("%d drawn headers and rows", n)
	}
}

// Plan Task 9 (design 6.3, review A-I2): on a ui that draws but keeps the
// text items its own (macOS), the headers, rows and the lines that cannot
// be clicked (the server line, the sign-in prompt) are drawn; newversion
// and the actions stay the menu's own items with their titles, check mark
// and enabled state; the problem line goes under its header (F8).
func TestMacDrawsThePanelAndKeepsTheActions(t *testing.T) {
	v := fixtureView(panelfixture.States())
	want := panel.Items(*v.Panel)
	r := drawnRun(t, true, "darwin")
	r.ui.nativeActions.Store(true)
	r.send(v)
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, items, _, _, _, _ := r.ui.snapshot()
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude = "+
		"h.codex r.codex.0 r.codex.1 r.codex.2 r.codex.3 r.codex.4 p.codex = server dashboard refresh autostart remove quit")
	for _, id := range []string{"h.claude", "r.claude.0", "r.claude.1", "r.claude.2", "h.codex", "r.codex.0", "r.codex.1"} {
		if items[id].drawn == nil || !items[id].visible {
			t.Fatalf("%s: drawn %v visible %v", id, items[id].drawn, items[id].visible)
		}
		eq(t, *items[id].drawn, want[id])
	}
	eq(t, *items["server"].drawn, panel.Item{Kind: "info", Text: v.Panel.Server, Dim: true})
	eq(t, items["p.codex"].visible, false)
	eq(t, items["h.codex"].drawn.Problem, text.CodexExpired)
	// a drawn item's own title is its view's label (review A3-M2): what
	// VoiceOver reads for the menu item and type-select matches, never
	// the text menu's bar characters
	for _, id := range []string{"h.claude", "r.claude.0", "r.claude.1", "r.claude.2", "h.codex", "r.codex.0", "r.codex.1", "server"} {
		eq(t, items[id].title, panel.AccessibleText(*items[id].drawn))
	}
	for _, id := range []string{"newversion", "dashboard", "refresh", "autostart", "remove", "quit"} {
		if items[id].drawnSets != 0 {
			t.Fatalf("%s is drawn: the actions stay the menu's own", id)
		}
	}
	eq(t, items["refresh"].title, "Refresh now")
	eq(t, items["refresh"].enabled, true)
	eq(t, items["autostart"].checked, true)
	eq(t, items["quit"].visible, true)

	// signed out: the prompt is drawn, wrapped like the server line
	out := panelfixture.States()
	out.Snap.Providers = map[provider.ID]status.ProviderStatus{provider.Claude: {State: "not_signed_in"}, provider.Codex: {State: "not_signed_in"}}
	r = drawnRun(t, true, "darwin")
	r.ui.nativeActions.Store(true)
	r.send(fixtureView(out))
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, items, _, _, _, _ = r.ui.snapshot()
	prompt := 0
	for _, id := range layout {
		if strings.HasPrefix(id, "p.") && items[id].visible {
			if items[id].drawn == nil || items[id].drawn.Kind != "info" {
				t.Fatalf("%s: the sign-in prompt is not drawn: %+v", id, items[id].drawn)
			}
			prompt++
		}
	}
	if prompt == 0 {
		t.Fatalf("no sign-in prompt in %v", layout)
	}
}

// A view without Codex rebuilds the drawn menu; one with fewer Claude
// rows hides the row that went; a changed row is drawn anew, an unchanged
// one is not set again.
func TestDrawnItemsFollowTheView(t *testing.T) {
	r := drawnRun(t, true, "windows")
	in := panelfixture.Combined()
	r.send(fixtureView(in))
	eq(t, r.next().Type, trayproto.TypeReady)
	_, items, _, _, _, _ := r.ui.snapshot()
	sets := items["r.claude.1"].drawnSets

	noCodex := panelfixture.Combined()
	delete(noCodex.Snap.Providers, provider.Codex)
	r.send(fixtureView(noCodex))
	// built and set: quit, the last item, is shown once every item before
	// it has its data (a check right after the build raced the setting)
	waitFor(t, "the rebuild", func() bool {
		layout, items, _, _, _, resets := r.ui.snapshot()
		return resets == 1 && !strings.Contains(strings.Join(layout, " "), "h.codex") && layout[len(layout)-1] == "quit" && items["quit"].visible
	})
	layout, items, _, _, _, _ := r.ui.snapshot()
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude = "+
		"server dashboard refresh autostart remove quit")
	if items["h.claude"].drawn == nil || items["r.claude.2"].drawn == nil || !items["r.claude.2"].visible {
		t.Fatal("the rebuilt menu is not drawn")
	}

	fewer := panelfixture.Combined()
	delete(fewer.Snap.Providers, provider.Codex)
	c := fewer.Snap.Providers[provider.Claude]
	c.Rows = c.Rows[:2] // no Fable
	c.Rows[0].Percent = new(float64)
	*c.Rows[0].Percent = 57
	fewer.Snap.Providers[provider.Claude] = c
	r.send(fixtureView(fewer))
	waitFor(t, "Fable to go", func() bool {
		_, items, _, _, _, _ := r.ui.snapshot()
		return !items["r.claude.2"].visible
	})
	_, items, _, _, _, resets := r.ui.snapshot()
	eq(t, resets, 1)
	eq(t, items["r.claude.0"].drawn.PercentText, "57%")
	eq(t, items["r.claude.1"].drawnSets, sets) // the same as before the rebuild: set once since
}

// A hidden item whose text changes stays hidden: the Windows tray library
// shows an item again when its title is set (upstream), so the helper hides
// it again (the problem line under a drawn header changes its text when
// the problem does).
func TestHiddenItemStaysHiddenWhenItsTextChanges(t *testing.T) {
	r := drawnRun(t, true, "windows")
	st := panelfixture.States()
	r.send(fixtureView(st))
	eq(t, r.next().Type, trayproto.TypeReady)
	other := panelfixture.States()
	x := other.Snap.Providers[provider.Codex]
	x.State = "unsupported_store"
	other.Snap.Providers[provider.Codex] = x
	v := fixtureView(other)
	r.send(v)
	waitFor(t, "the new problem", func() bool {
		_, items, _, _, _, _ := r.ui.snapshot()
		return items["h.codex"].drawn != nil && items["h.codex"].drawn.Problem != text.CodexExpired
	})
	_, items, _, _, _, _ := r.ui.snapshot()
	sets := items["p.codex"].sets
	eq(t, items["p.codex"].visible, false)
	if n := len(sets); n < 2 || sets[n-1] != "visible=false" {
		t.Fatalf("p.codex calls %v: not hidden after its title", sets)
	}
}

// Every visible item is drawn in the states the CI menu never shows (review
// B-M10): the new-version line, the sign-in prompt, "Refreshing…".
func TestEveryVisibleItemIsDrawn(t *testing.T) {
	signedOut := panelfixture.States()
	signedOut.Snap.Providers = map[provider.ID]status.ProviderStatus{provider.Claude: {State: "not_signed_in"}, provider.Codex: {State: "not_signed_in"}}
	newer := panelfixture.Combined()
	newer.Snap.ClientLatestVersion = "99.0.0"
	busy := panelfixture.Combined()
	busy.Snap.FreshPending = true
	for name, in := range map[string]tray.MenuInputs{"signed out": signedOut, "new version": newer, "refreshing": busy} {
		r := drawnRun(t, true, "windows")
		v := fixtureView(in)
		r.send(v)
		eq(t, r.next().Type, trayproto.TypeReady)
		layout, items, _, _, _, _ := r.ui.snapshot()
		visible := 0
		for _, id := range layout {
			if id == "=" || !items[id].visible {
				continue
			}
			visible++
			if items[id].drawn == nil {
				t.Fatalf("%s: %s is visible and not drawn (%v)", name, id, layout)
			}
		}
		var want string
		switch name {
		case "signed out":
			want = "p."
		case "new version":
			want = "newversion"
		case "refreshing":
			want = "refresh"
		}
		found := false
		for _, id := range layout {
			if strings.HasPrefix(id, want) && items[id].visible && items[id].drawn != nil {
				found = true
				if name == "refreshing" && items[id].drawn.Enabled {
					t.Fatalf("Refreshing… drawn enabled: %+v", items[id].drawn)
				}
			}
		}
		if !found || visible < 6 {
			t.Fatalf("%s: no drawn %s among %v", name, want, layout)
		}
	}
}

// Review A-m4: when the ui stops drawing (the Windows drawer panicked),
// the helper builds the text menu of today within a tick or two, without
// waiting for a new view.
func TestTextMenuWhenTheUIStopsDrawing(t *testing.T) {
	r := drawnRun(t, true, "windows")
	v := fixtureView(panelfixture.States())
	r.send(v)
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, _, _, _, _, _ := r.ui.snapshot()
	if !strings.Contains(strings.Join(layout, " "), "=") {
		t.Fatalf("not drawn first: %v", layout)
	}
	r.ui.draws.Store(false)
	waitFor(t, "the text menu", func() bool {
		layout, items, _, _, _, resets := r.ui.snapshot()
		return resets == 1 && !strings.Contains(strings.Join(layout, " "), "=") && layout[len(layout)-1] == "quit" && items["quit"].visible
	})
	layout, items, _, _, _, _ := r.ui.snapshot()
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude - "+
		"h.codex r.codex.0 r.codex.1 r.codex.2 r.codex.3 r.codex.4 p.codex - server - dashboard refresh autostart remove quit")
	for id := range items {
		if items[id].drawn != nil {
			t.Fatalf("%s still drawn", id)
		}
	}
	eq(t, items["p.codex"].visible, true)
	eq(t, items["p.codex"].title, text.CodexExpired)
}

// Review A-m1: a drawing failure shows the text menu at once, not at the
// helper's next tick (a menu opened meanwhile would be items that nobody
// draws).
func TestTextMenuAtOnceWhenTheDrawingFails(t *testing.T) {
	ui := newFakeUI()
	ui.draws.Store(true)
	ui.drawFailed = make(chan struct{})
	r := startRun(t, runOpts{goos: "windows", ui: ui, tick: time.Hour})
	r.hello()
	r.send(fixtureView(panelfixture.States()))
	eq(t, r.next().Type, trayproto.TypeReady)
	layout, _, _, _, _, _ := r.ui.snapshot()
	if !strings.Contains(strings.Join(layout, " "), "=") {
		t.Fatalf("not drawn first: %v", layout)
	}
	ui.draws.Store(false)
	close(ui.drawFailed)
	waitFor(t, "the text menu", func() bool {
		layout, items, _, _, _, resets := r.ui.snapshot()
		return resets == 1 && !strings.Contains(strings.Join(layout, " "), "=") && layout[len(layout)-1] == "quit" && items["quit"].visible
	})
}

// Review B3-m13, A3-M2: the same on macOS, whose actions stay the menu's
// own: after the drawer failed the problem line is a text item again and
// every item's title is the text menu's line again.
func TestMacTextMenuWhenTheUIStopsDrawing(t *testing.T) {
	r := drawnRun(t, true, "darwin")
	r.ui.nativeActions.Store(true)
	v := fixtureView(panelfixture.States())
	r.send(v)
	eq(t, r.next().Type, trayproto.TypeReady)
	_, items, _, _, _, _ := r.ui.snapshot()
	if d := items["h.codex"].drawn; d == nil || items["h.codex"].title != panel.AccessibleText(*d) {
		t.Fatalf("not drawn first: %q %+v", items["h.codex"].title, d)
	}
	r.ui.draws.Store(false)
	waitFor(t, "the text menu", func() bool {
		layout, items, _, _, _, resets := r.ui.snapshot()
		return resets == 1 && !strings.Contains(strings.Join(layout, " "), "=") && layout[len(layout)-1] == "quit" && items["quit"].visible
	})
	layout, items, _, _, _, _ := r.ui.snapshot()
	eq(t, strings.Join(layout, " "), "newversion h.claude r.claude.0 r.claude.1 r.claude.2 r.claude.3 r.claude.4 p.claude - "+
		"h.codex r.codex.0 r.codex.1 r.codex.2 r.codex.3 r.codex.4 p.codex - server - dashboard refresh autostart remove quit")
	byID := map[string]trayproto.Item{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	for id := range items {
		if items[id].drawn != nil {
			t.Fatalf("%s still drawn", id)
		}
		if byID[id].Visible {
			eq(t, items[id].title, byID[id].Text)
		}
	}
	eq(t, items["p.codex"].visible, true)
	eq(t, items["p.codex"].title, text.CodexExpired)
}

// Review A-m8: on a ui that does not draw (Linux, macOS), a hidden item
// whose text changes gets its title and nothing else: the menu is exactly
// as before the drawn panel.
func TestHiddenItemTitleOnlyWhereTheLibraryShowsIt(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		r := drawnRun(t, false, goos)
		in := panelfixture.Combined()
		r.send(fixtureView(in))
		eq(t, r.next().Type, trayproto.TypeReady)
		_, items, _, _, _, _ := r.ui.snapshot()
		before := len(items["newversion"].sets)
		eq(t, items["newversion"].visible, false)
		v := fixtureView(in)
		for i := range v.Items {
			if v.Items[i].ID == "newversion" {
				v.Items[i].Text = "Version 9.9.9 is out (hidden)"
			}
		}
		r.send(v)
		waitFor(t, "the new title", func() bool {
			_, items, _, _, _, _ := r.ui.snapshot()
			return items["newversion"].title == "Version 9.9.9 is out (hidden)"
		})
		_, items, _, _, _, _ = r.ui.snapshot()
		eq(t, items["newversion"].sets[before:], []string{"title"})
	}
}

// RF4: the drawing data of an item is swapped whole while it is read.
func TestDrawStoreWhileViewChanges(t *testing.T) {
	var s drawStore
	a := panel.Items(tray.BuildPanel(panelfixture.Combined()))
	b := panel.Items(tray.BuildPanel(panelfixture.States()))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			src := a
			if i%2 == 1 {
				src = b
			}
			for _, id := range []string{"h.codex", "r.codex.0", "r.codex.1"} {
				it := src[id]
				s.Set(uint32(len(id)), &it)
			}
			if i%10 == 9 {
				s.Set(9, nil)
			}
		}
	}()
	m := panel.MetricsAt(96)
	for range 1000 {
		for _, id := range []uint32{7, 9} {
			it, ok := s.Get(id)
			if !ok {
				continue
			}
			w, h := panel.Size(it, m, testMeasurer{})
			_ = panel.Layout(it, m, testMeasurer{}, w, h, panel.LightPalette, true)
		}
	}
	wg.Wait()
}

type testMeasurer struct{}

func (testMeasurer) TextWidth(_ panel.Font, s string) int { return 7 * len([]rune(s)) }
func (testMeasurer) LineHeight(panel.Font) int            { return 16 }

// One switch pins the panel to light: off, no mode is read and nothing is
// dark; on, apps in dark mode get the dark panel, anything unclear the
// light one, and high contrast the system's colours (light).
func TestDarkOnlyWhenAppsAreDark(t *testing.T) {
	reads := 0
	read := func(v uint32, err error) func() (uint32, error) {
		return func() (uint32, error) { reads++; return v, err }
	}
	no := func() bool { return false }
	eq(t, themeFor(false, read(0, nil), no), menuTheme{})
	eq(t, reads, 0)
	eq(t, themeFor(true, read(0, nil), no), menuTheme{Dark: true})
	eq(t, themeFor(true, read(1, nil), no), menuTheme{})
	eq(t, themeFor(true, read(0, errors.New("no such value")), no), menuTheme{})
	eq(t, themeFor(true, read(0, nil), func() bool { return true }), menuTheme{})
	eq(t, menuTheme{Dark: true}.palette(panel.LightPalette), panel.DarkPalette)
	light := panel.LightPalette
	light.Background.R = 0xee // the system's own
	eq(t, menuTheme{}.palette(light), light)
	eq(t, followDarkMode, true)
}

// Review N1: an open of the menu while a view is applied takes the new
// view or the old one, never a mix (a header from one, rows from the
// other): the data of every drawn item that changes is set at once,
// before any item is switched.
func TestAnOpenSeesOneViewOrTheOther(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		t.Run(goos, func(t *testing.T) { anOpenSeesOneViewOrTheOther(t, goos) })
	}
}

// anOpenSeesOneViewOrTheOther: on macOS (review A3-M1) the snapshot taken
// at MenuOpening comes from the one step that sets every changed item.
func anOpenSeesOneViewOrTheOther(t *testing.T, goos string) {
	r := drawnRun(t, true, goos)
	r.ui.nativeActions.Store(goos == "darwin")
	r.send(fixtureView(panelfixture.Combined()))
	eq(t, r.next().Type, trayproto.TypeReady)
	old := r.ui.store.snapshot()
	r.ui.takeProbes()

	in := panelfixture.Combined()
	c := in.Snap.Providers[provider.Claude]
	for i, p := range []float64{57, 21} {
		c.Rows[i].Percent = new(float64)
		*c.Rows[i].Percent = p
	}
	in.Snap.Providers[provider.Claude] = c
	x := in.Snap.Providers[provider.Codex]
	x.Rows[0].Percent = new(float64)
	*x.Rows[0].Percent = 3
	in.Snap.Providers[provider.Codex] = x
	r.send(fixtureView(in))
	waitFor(t, "the new view", func() bool {
		_, items, _, _, _, _ := r.ui.snapshot()
		return items["r.codex.0"].drawn != nil && items["r.codex.0"].drawn.PercentText == "3%"
	})
	now := r.ui.store.snapshot()
	var changed []uint32
	for id, it := range now {
		if o, ok := old[id]; !ok || !reflect.DeepEqual(o, it) {
			changed = append(changed, id)
		}
	}
	if len(changed) < 3 {
		t.Fatalf("only %d items changed", len(changed))
	}
	probes := r.ui.takeProbes()
	if len(probes) == 0 {
		t.Fatal("no write to the drawer's store")
	}
	for n, p := range probes {
		news := 0
		for _, id := range changed {
			if reflect.DeepEqual(p[id], now[id]) {
				news++
			} else if !reflect.DeepEqual(p[id], old[id]) {
				t.Fatalf("probe %d: item %d is neither the old nor the new view", n, id)
			}
		}
		if news != 0 && news != len(changed) {
			t.Fatalf("probe %d: an open would take %d of the %d changed items new, the others old", n, news, len(changed))
		}
	}
}
