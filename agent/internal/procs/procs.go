// Package procs finds and stops other copies of the agent and holds the
// single-instance lock.
package procs

import "errors"

// ErrUnsupported: process lookup is not available on this OS.
var ErrUnsupported = errors.New("procs: not supported on this OS")

// killWait is how long Kill waits for the processes to end.
const killWait = 5
