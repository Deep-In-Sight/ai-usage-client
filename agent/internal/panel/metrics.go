package panel

// Metrics are the panel's sizes in pixels at one DPI: its width, the
// heights of a header, a problem line and a limit row, the side padding
// and the bar's height.
type Metrics struct{ Width, Header, Problem, Row, Pad, Bar int }

// at96 are the sizes at 96 DPI (100 %, design 6.2's starting values).
var at96 = Metrics{Width: 320, Header: 32, Problem: 28, Row: 64, Pad: 12, Bar: BarHeight}

// MetricsAt are the sizes at dpi: those at 96 DPI scaled by dpi/96 and
// rounded (an unknown DPI, 0 or less, is 96).
func MetricsAt(dpi int) Metrics {
	if dpi <= 0 {
		dpi = 96
	}
	s := func(v int) int { return (v*dpi + 48) / 96 }
	m := at96
	return Metrics{Width: s(m.Width), Header: s(m.Header), Problem: s(m.Problem), Row: s(m.Row), Pad: s(m.Pad), Bar: s(m.Bar)}
}
