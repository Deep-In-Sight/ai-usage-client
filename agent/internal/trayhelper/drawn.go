package trayhelper

import (
	"strings"
	"sync"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// drawnItems is what each visible item of a view's menu draws as when the
// ui draws the usage panel (design 5.1, 6.2, 6.3): the panel's headers and
// rows (panel.Items), the server line with the panel's text and the
// sign-in prompt (lines that cannot be clicked: they wrap at the panel's
// width, review A-I2), and with actions (Windows, stage 0: every item is
// drawn) newversion and the actions as text items; without (macOS) those
// stay the menu's own items. A section's problem line is not among them:
// its header draws it.
func drawnItems(p trayproto.Panel, byID map[string]trayproto.Item, actions bool) map[string]panel.Item {
	out := panel.Items(p)
	for _, id := range trayproto.ItemIDs {
		it, ok := byID[id]
		if _, drawn := out[id]; drawn || !ok || !it.Visible || hiddenUnderHeader(id, out) {
			continue
		}
		switch kind, _, _ := strings.Cut(id, "."); {
		case id == "server":
			out[id] = panel.Item{Kind: "info", Text: p.Server, Dim: true}
		case kind == "p": // the sign-in prompt (no section is drawn)
			out[id] = panel.Item{Kind: "info", Text: it.Text}
		case actions: // newversion and the actions (and anything the panel lacks)
			out[id] = panel.Item{Kind: "action", Text: it.Text, Enabled: it.Enabled, Checkbox: it.Checkbox, Checked: it.Checked}
		}
	}
	return out
}

// hiddenUnderHeader: id is a section's problem line and that section's
// header is drawn, which draws the line under itself (design 6.2: the
// line goes between the header and the rows, ItemIDs keep their order).
func hiddenUnderHeader(id string, drawn map[string]panel.Item) bool {
	p, ok := strings.CutPrefix(id, "p.")
	if !ok {
		return false
	}
	_, ok = drawn["h."+p]
	return ok
}

// drawStore is what each drawn item draws, by its menu id. The helper sets
// an item's data whole while the tray's thread may be drawing it (the menu
// can be open across a new view, RF4): a draw reads one version or the
// other, never a mix; and it sets the data of all the items a view changes
// at once (SetAll), so an open takes one view or the other (review N1).
type drawStore struct {
	mu   sync.Mutex
	byID map[uint32]panel.Item
}

// Set keeps it for id (nil: forget it).
func (s *drawStore) Set(id uint32, it *panel.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it == nil {
		delete(s.byID, id)
		return
	}
	if s.byID == nil {
		s.byID = map[uint32]panel.Item{}
	}
	s.byID[id] = *it
}

// SetAll keeps each item's data, all under one lock: a snapshot taken
// meanwhile has all of them or none (review N1).
func (s *drawStore) SetAll(items map[uint32]panel.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = map[uint32]panel.Item{}
	}
	for id, it := range items {
		s.byID[id] = it
	}
}

// Clear forgets every item (the menu was rebuilt: its ids are not used
// again).
func (s *drawStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID = nil
}

// snapshot is a copy of what every item draws now.
func (s *drawStore) snapshot() map[uint32]panel.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[uint32]panel.Item, len(s.byID))
	for id, it := range s.byID {
		out[id] = it
	}
	return out
}

// Get is what id draws (false: nothing kept).
func (s *drawStore) Get(id uint32) (panel.Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.byID[id]
	return it, ok
}
