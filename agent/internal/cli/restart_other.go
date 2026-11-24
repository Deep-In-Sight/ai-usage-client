//go:build !linux && !darwin

package cli

import "errors"

// restartSelf: only Linux loses its tray with the session bus.
func restartSelf([]string, int) error { return errors.New("restart: not supported on this OS") }
