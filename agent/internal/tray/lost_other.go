//go:build !linux

package tray

// sessionLost: only the Linux tray depends on a connection that can go.
func sessionLost() <-chan struct{} { return nil }
