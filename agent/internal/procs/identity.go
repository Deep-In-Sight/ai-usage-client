package procs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Identity names a running process: its pid and the kernel's record of
// when it started, so that a pid reused by another process is not mistaken
// for it. Start counts from an OS-specific origin; only order and equality
// matter.
type Identity struct {
	PID   int
	Start uint64
}

// Older: a started before b (a pid breaks a tie, so this is a total order).
func (a Identity) Older(b Identity) bool {
	if a.Start != b.Start {
		return a.Start < b.Start
	}
	return a.PID < b.PID
}

// String is the form kept in the lock file: "<pid> <start>".
func (a Identity) String() string { return fmt.Sprintf("%d %d", a.PID, a.Start) }

func parseIdentity(s string) (Identity, bool) {
	var id Identity
	n, err := fmt.Sscanf(strings.TrimSpace(s), "%d %d", &id.PID, &id.Start)
	return id, err == nil && n == 2 && id.PID > 0
}

// Self is this process's identity.
func Self() (Identity, error) { return IdentityOf(os.Getpid()) }

// Holder is the identity the lock file at path names, if that process is
// still the one that wrote it.
func Holder(path string) (Identity, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, false
	}
	id, ok := parseIdentity(string(b))
	if !ok {
		return Identity{}, false
	}
	now, err := IdentityOf(id.PID)
	return id, err == nil && now == id
}

// ErrNewer: the lock is held by a process that started after this one.
var ErrNewer = errors.New("procs: the lock is held by a newer process")

// TakeOverOptions are the waits of TakeOver (zero values: the defaults).
type TakeOverOptions struct {
	TermWait time.Duration // after SIGTERM, before SIGKILL (10 s)
	KillWait time.Duration // after SIGKILL, before giving up (5 s)
}

func (o TakeOverOptions) withDefaults() TakeOverOptions {
	if o.TermWait <= 0 {
		o.TermWait = 10 * time.Second
	}
	if o.KillWait <= 0 {
		o.KillWait = 5 * time.Second
	}
	return o
}
