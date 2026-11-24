package tray

import "fyne.io/systray"

// setNextToIcon sets the label Ubuntu's AppIndicator extension shows next
// to the icon (the patched library in third_party/systray).
func setNextToIcon(t string) { systray.SetLabel(t, LabelGuide) }
