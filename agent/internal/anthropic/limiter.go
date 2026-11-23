package anthropic

import (
	"sync"
	"time"
)

const (
	backoffBase = 120 * time.Second
	backoffMax  = 600 * time.Second
	minGap      = 10 * time.Second
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

// Limiter keeps fetches apart: the 429 backoff, and at least 10 s after any
// other result.
type Limiter struct {
	mu   sync.Mutex
	n429 int
	next time.Time
}

// Allowed reports whether a fetch may start at now.
func (l *Limiter) Allowed(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !now.Before(l.next)
}

// Record notes a fetch result.
func (l *Limiter) Record(r Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.HTTPStatus == 429 {
		l.n429++
		l.next = r.FetchedAt.Add(Backoff(l.n429, r.RetryAfter))
		return
	}
	l.n429 = 0
	l.next = r.FetchedAt.Add(minGap)
}

// NextAllowed is the earliest time the next fetch may start.
func (l *Limiter) NextAllowed() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.next
}
