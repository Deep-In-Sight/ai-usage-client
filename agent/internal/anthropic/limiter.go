package anthropic

import (
	"sync"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/config"
)

const (
	backoffBase = 120 * time.Second
	backoffMax  = 600 * time.Second

	// MinGap is the shortest time between two fetches, whatever a server says.
	MinGap = 60 * time.Second
	// HourlyCap is the most fetches in any rolling hour (the server's own
	// per-account budget), whatever a server says.
	HourlyCap = 15
	window    = time.Hour
)

// Backoff is the wait after the n-th 429 in a row:
// min(600 s, max(120 s × 2^(n−1), Retry-After)).
func Backoff(consecutive429 int, retryAfter time.Duration) time.Duration {
	if consecutive429 < 1 {
		return 0
	}
	d := backoffMax
	if consecutive429 <= 4 {
		d = backoffBase << (consecutive429 - 1)
	}
	if retryAfter > d {
		d = retryAfter
	}
	if d > backoffMax {
		d = backoffMax
	}
	return d
}

// Limiter is the agent's own ceiling on fetches, which no server answer can
// lift: the 429 backoff, at least MinGap between fetches, and at most
// HourlyCap fetches in any rolling hour. Its record can be saved and restored
// so a restart does not reset it.
type Limiter struct {
	mu       sync.Mutex
	n429     int
	next     time.Time
	attempts []time.Time // oldest first, at most HourlyCap
}

// prune drops attempts older than the window and limits the effect of a wall
// clock that was set back: nothing recorded may wait longer than the longest
// rule would from now.
func (l *Limiter) pruneLocked(now time.Time) {
	kept := l.attempts[:0]
	for _, a := range l.attempts {
		if a.After(now) {
			a = now
		}
		if now.Sub(a) < window {
			kept = append(kept, a)
		}
	}
	l.attempts = kept
	if limit := now.Add(backoffMax); l.next.After(limit) {
		l.next = limit
	}
}

func (l *Limiter) nextLocked() time.Time {
	next := l.next
	if len(l.attempts) >= HourlyCap {
		if t := l.attempts[len(l.attempts)-HourlyCap].Add(window); t.After(next) {
			next = t
		}
	}
	return next
}

// Allowed reports whether a fetch may start at now.
func (l *Limiter) Allowed(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	return !now.Before(l.nextLocked())
}

// Record notes a fetch attempt and its result.
func (l *Limiter) Record(r Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts = append(l.attempts, r.FetchedAt)
	if n := len(l.attempts); n > HourlyCap {
		l.attempts = append([]time.Time(nil), l.attempts[n-HourlyCap:]...)
	}
	gap := MinGap
	if r.HTTPStatus == 429 {
		l.n429++
		gap = max(gap, Backoff(l.n429, r.RetryAfter))
	} else {
		l.n429 = 0
	}
	l.next = r.FetchedAt.Add(gap)
}

// NextAllowed is the earliest time the next fetch may start.
func (l *Limiter) NextAllowed() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextLocked()
}

// Snapshot is the record to save.
func (l *Limiter) Snapshot() config.FetchLimit {
	l.mu.Lock()
	defer l.mu.Unlock()
	return config.FetchLimit{Attempts: append([]time.Time(nil), l.attempts...), NotBefore: l.next, Consecutive429: l.n429}
}

// Restore loads a saved record.
func (l *Limiter) Restore(s config.FetchLimit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts = append([]time.Time(nil), s.Attempts...)
	if n := len(l.attempts); n > HourlyCap {
		l.attempts = l.attempts[n-HourlyCap:]
	}
	l.next = s.NotBefore
	l.n429 = s.Consecutive429
}
