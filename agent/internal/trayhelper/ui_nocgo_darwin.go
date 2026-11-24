//go:build darwin && !cgo

package trayhelper

// Compiled is false: the macOS tray needs cgo (Cocoa).
const Compiled = false

func newUI(Options) ui { return nil }
