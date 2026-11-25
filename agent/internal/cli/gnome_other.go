//go:build !linux

package cli

// The GNOME Shell extension is Linux only (pre-flight ruling F16): on
// other systems install writes none and removal has none to remove.

func installPanelExtension(Env) (first bool) { return false }

func removePanelExtension(Env) error { return nil }
