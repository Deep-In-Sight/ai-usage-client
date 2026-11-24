//go:build !linux && !darwin

package tray

// setNextToIcon: nothing is shown next to the icon (Windows).
func setNextToIcon(string) {}
