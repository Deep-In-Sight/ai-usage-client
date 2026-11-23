//go:build darwin && !cgo

package tray

import "context"

// Compiled is false: the macOS tray needs cgo (Cocoa).
const Compiled = false

// Run returns ErrNoTray.
func Run(context.Context, Controller, <-chan struct{}, string) error { return ErrNoTray }
