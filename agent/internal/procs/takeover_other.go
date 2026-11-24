//go:build !linux && !darwin

package procs

import "context"

// TakeOver is a plain LockAs here: on Windows signing out ends every
// process, so no instance outlives its session and none is taken over.
func TakeOver(_ context.Context, path string, self Identity, _ TakeOverOptions) (unlock func(), replaced Identity, err error) {
	unlock, err = LockAs(path, self)
	return unlock, Identity{}, err
}
