// Package clock abstracts time so the agent's timing rules can be tested
// with a fake clock.
package clock

import (
	"sync"
	"time"
)

// Clock is the time source used by the agent.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time                         { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

type waiter struct {
	at time.Time
	ch chan time.Time
}

// Fake is a manually advanced clock. Channels returned by After fire when
// the fake time reaches their deadline.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

// NewFake returns a fake clock set to t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := f.now.Add(d)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, waiter{at: at, ch: ch})
	return ch
}

// Advance moves the fake time forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	t := f.now.Add(d)
	f.mu.Unlock()
	f.Set(t)
}

// Next is the earliest deadline still waited for (false: none), so a test
// can step the time from one timer to the next.
func (f *Fake) Next() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var next time.Time
	for _, w := range f.waiters {
		if next.IsZero() || w.at.Before(next) {
			next = w.at
		}
	}
	return next, !next.IsZero()
}

// Set moves the fake time to t and fires every waiter that is due.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.at.After(t) {
			w.ch <- t
			continue
		}
		kept = append(kept, w)
	}
	f.waiters = kept
}
