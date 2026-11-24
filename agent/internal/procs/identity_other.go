//go:build !linux && !darwin

package procs

// IdentityOf is not available on this OS.
func IdentityOf(int) (Identity, error) { return Identity{}, ErrUnsupported }
