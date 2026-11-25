// Package panelcheck tells tests whether a drawn item shows each of its
// texts whole. panel.Layout clamps every step into the item, so "every
// step lies inside" cannot fail; a text that was cut short, squeezed into
// a box lower than its line or dropped must (review B-I1). Only tests
// import it.
package panelcheck

import (
	"fmt"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"
)

// Want is a text an item must show, in its font.
type Want struct {
	Text string
	Font panel.Font
}

// Texts are the texts it draws: a header's name, account, time and each
// line of its problem; a row's label, percent and reset line; "More
// limits"; an action's text; each line of an info item.
func Texts(it panel.Item, m panel.Metrics, tm panel.Measurer) []Want {
	var out []Want
	add := func(s string, f panel.Font) {
		if s != "" {
			out = append(out, Want{s, f})
		}
	}
	lines := func(s string) {
		for _, l := range panel.Wrap(s, panel.Regular, m.Width-2*m.Pad, tm) {
			add(l, panel.Regular)
		}
	}
	switch it.Kind {
	case "header":
		add(it.Name, panel.Bold)
		add(it.Account, panel.Regular)
		add(it.Updated, panel.Regular)
		lines(it.Problem)
	case "row":
		add(it.Label, panel.SemiBold)
		add(it.PercentText, panel.Bold)
		add(it.Detail, panel.Regular)
	case "more":
		add(it.Label, panel.Regular)
	case "action":
		add(it.Text, panel.Regular)
	case "info":
		lines(it.Text)
	}
	return out
}

// Missing lists each text of it that ops (drawn in a w×h item) do not show
// whole: no text step with it in its font, or one narrower than the text,
// lower than the font's line, or not inside the item.
func Missing(it panel.Item, ops []panel.Op, m panel.Metrics, tm panel.Measurer, w, h int) []string {
	var out []string
	for _, want := range Texts(it, m, tm) {
		var found *panel.Op
		for i, o := range ops {
			if o.Kind == panel.Text && o.Text == want.Text && o.Font == want.Font {
				found = &ops[i]
				break
			}
		}
		if found == nil {
			out = append(out, fmt.Sprintf("%s %q not drawn", it.Kind, want.Text))
			continue
		}
		o := *found
		tw, lh := tm.TextWidth(want.Font, want.Text), tm.LineHeight(want.Font)
		switch {
		case o.W < tw:
			out = append(out, fmt.Sprintf("%s %q in %d px, it needs %d", it.Kind, want.Text, o.W, tw))
		case o.H < lh:
			out = append(out, fmt.Sprintf("%s %q in a box %d px high, its line is %d", it.Kind, want.Text, o.H, lh))
		case o.X < 0 || o.Y < 0 || o.X+o.W > w || o.Y+o.H > h:
			out = append(out, fmt.Sprintf("%s %q at %d,%d %dx%d, outside %dx%d", it.Kind, want.Text, o.X, o.Y, o.W, o.H, w, h))
		}
	}
	return out
}
