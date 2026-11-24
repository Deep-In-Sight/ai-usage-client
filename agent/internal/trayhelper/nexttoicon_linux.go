package trayhelper

import (
	"fyne.io/systray"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
)

// setNextToIcon sets the label Ubuntu's AppIndicator extension shows next
// to the icon (the patched library in third_party/systray).
func setNextToIcon(t string) { systray.SetLabel(t, tray.LabelGuide) }
