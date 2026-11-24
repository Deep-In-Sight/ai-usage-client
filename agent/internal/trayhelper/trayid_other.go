//go:build !windows && (!darwin || cgo)

package trayhelper

// setTrayID: only Windows identifies the icon by a number.
func setTrayID(uint32) {}

// iconShown: the Linux and macOS libraries have no "not added yet" state
// (Linux waits for the tray host before it starts).
func iconShown() bool { return true }
