package panel

// Metrics are the panel's sizes in pixels at one DPI: its width (as
// measured; Windows adds its own gutter to a menu item's width), the
// heights of a header, a problem line, a limit row, a text item (an
// action, the server line, "More limits") and a separator, the side
// padding, the bar's height, the header picture's size and the width of
// the check-mark column in front of the actions.
type Metrics struct {
	Width, Header, Problem, Row, Pad, Bar int
	Item, Separator, Picture, Check       int
}

// at96 are the sizes at 96 DPI (100 %): design 6.2's starting values
// tuned once to the approved mock's Windows column (plan Task 8): a 300 px
// bar between 16 px pads plus Windows' gutter, 64 px rows, 30 px text
// items, an 18 px picture.
var at96 = Metrics{Width: 320, Header: 32, Problem: 28, Row: 64, Pad: 16, Bar: BarHeight,
	Item: 30, Separator: 13, Picture: 18, Check: 22}

// MetricsAt are the sizes at dpi: those at 96 DPI scaled by dpi/96 and
// rounded (an unknown DPI, 0 or less, is 96).
func MetricsAt(dpi int) Metrics {
	if dpi <= 0 {
		dpi = 96
	}
	s := func(v int) int { return (v*dpi + 48) / 96 }
	m := at96
	return Metrics{Width: s(m.Width), Header: s(m.Header), Problem: s(m.Problem), Row: s(m.Row), Pad: s(m.Pad), Bar: s(m.Bar),
		Item: s(m.Item), Separator: s(m.Separator), Picture: s(m.Picture), Check: s(m.Check)}
}

// px is n pixels at 96 DPI at these metrics' DPI (as the padding scales).
func (m Metrics) px(n int) int { return (n*m.Pad + 8) / 16 }
