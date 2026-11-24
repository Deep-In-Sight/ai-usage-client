//go:build unix

package procs

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrLocked: another process holds the lock.
var ErrLocked = errors.New("procs: lock is held by another process")

// Lock takes an exclusive, non-blocking lock on path (created if needed).
// The lock goes away when unlock is called or the process ends.
func Lock(path string) (unlock func(), err error) {
	f, err := lockFile(path)
	if err != nil {
		return nil, err
	}
	return unlockFunc(f), nil
}

// LockAs is Lock that also writes id into the file, so that a newer run
// can tell who holds it (see TakeOver).
func LockAs(path string, id Identity) (unlock func(), err error) {
	f, err := lockFile(path)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(id.String()+"\n"), 0)
	}
	return unlockFunc(f), nil
}

func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}

func unlockFunc(f *os.File) func() {
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}
}
