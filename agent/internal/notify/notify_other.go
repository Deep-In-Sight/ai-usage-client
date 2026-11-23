//go:build !linux && !windows

package notify

// Show is not available on this OS.
func Show(title, body string) error { return ErrUnsupported }
