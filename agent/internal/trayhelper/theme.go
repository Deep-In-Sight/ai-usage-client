package trayhelper

import "github.com/Deep-In-Sight/ai-usage-client/agent/internal/panel"

// followDarkMode is the one switch for the drawn panel's dark mode: true,
// the panel follows the system's app mode (light colours when apps are
// light, dark when they are dark, and again when it changes while the
// helper runs); false pins it light: no mode is read and nothing dark is
// drawn.
const followDarkMode = true

// menuTheme is the mode the drawn panel is in.
type menuTheme struct{ Dark bool }

// themeFor decides the mode: dark only when the switch is on, high
// contrast is off (its colours are the system's, the light path) and the
// app mode reads as dark (AppsUseLightTheme = 0); anything unclear is
// light. appsLight is not called when the switch is off.
func themeFor(follow bool, appsLight func() (uint32, error), highContrast func() bool) menuTheme {
	if !follow || (highContrast != nil && highContrast()) {
		return menuTheme{}
	}
	v, err := appsLight()
	if err != nil {
		return menuTheme{}
	}
	return menuTheme{Dark: v == 0}
}

// palette is the colours to draw with: the dark set, or light (the
// system's menu colours).
func (t menuTheme) palette(light panel.Palette) panel.Palette {
	if t.Dark {
		return panel.DarkPalette
	}
	return light
}
