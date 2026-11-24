//go:build !linux && !darwin

package trayhelper

// setNextToIcon: nothing is shown next to the icon (Windows).
func setNextToIcon(string) {}
