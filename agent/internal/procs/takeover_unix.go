//go:build linux || darwin

package procs

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// TakeOver takes the lock at path for self (whose identity it then holds),
// taking it from an older holder: "the newest instance wins". The holder
// is the process the lock file names (see LockAs), checked to still be
// that process. If it started before self it is asked to stop (SIGTERM)
// and, if it still holds the lock after TermWait, killed (SIGKILL); TakeOver
// then waits up to KillWait more. A holder that started after self is left
// alone (ErrNewer, with its identity). A holder the file does not name (an
// older agent, or one that has not written its name yet) is never
// signalled; TakeOver waits for the lock and gives up with ErrLocked after
// TermWait + KillWait. replaced is the holder that was stopped, if any.
func TakeOver(ctx context.Context, path string, self Identity, o TakeOverOptions) (unlock func(), replaced Identity, err error) {
	o = o.withDefaults()
	deadline := time.Now().Add(o.TermWait + o.KillWait)
	var termAt time.Time
	var killed bool
	for {
		unlock, err := LockAs(path, self)
		if err == nil {
			return unlock, replaced, nil
		}
		if !errors.Is(err, ErrLocked) {
			return nil, Identity{}, err
		}
		if h, ok := Holder(path); ok && h != self {
			if self.Older(h) {
				return nil, h, ErrNewer
			}
			switch {
			case h != replaced:
				// a new holder: another older run took the lock after the
				// one asked before had stopped
				if unix.Kill(h.PID, unix.SIGTERM) == nil {
					replaced, termAt, killed = h, time.Now(), false
					deadline = termAt.Add(o.TermWait + o.KillWait)
				}
			case !killed && time.Since(termAt) >= o.TermWait:
				_ = unix.Kill(h.PID, unix.SIGKILL)
				killed = true
			}
		}
		if time.Now().After(deadline) {
			return nil, Identity{}, ErrLocked
		}
		select {
		case <-ctx.Done():
			return nil, Identity{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
