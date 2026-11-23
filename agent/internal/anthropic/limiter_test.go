package anthropic

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	var l Limiter
	eq(t, l.Allowed(base), true) // nothing recorded yet
	l.Record(Result{FetchedAt: base, HTTPStatus: 200})
	eq(t, l.Allowed(base.Add(9*time.Second)), false)
	eq(t, l.Allowed(base.Add(10*time.Second)), true)

	t1 := base.Add(time.Minute)
	l.Record(Result{FetchedAt: t1, HTTPStatus: 429})
	t2 := t1.Add(120 * time.Second)
	eq(t, l.Allowed(t2.Add(-time.Second)), false)
	l.Record(Result{FetchedAt: t2, HTTPStatus: 429})
	eq(t, l.Allowed(t2.Add(239*time.Second)), false)
	eq(t, l.Allowed(t2.Add(240*time.Second)), true)
	eq(t, l.NextAllowed(), t2.Add(240*time.Second))

	t3 := t2.Add(300 * time.Second)
	l.Record(Result{FetchedAt: t3, HTTPStatus: 200}) // a success resets the level
	t4 := t3.Add(time.Minute)
	l.Record(Result{FetchedAt: t4, HTTPStatus: 429})
	eq(t, l.NextAllowed(), t4.Add(120*time.Second))

	l.Record(Result{FetchedAt: t4, HTTPStatus: 0}) // other results: 10 s
	eq(t, l.NextAllowed(), t4.Add(10*time.Second))
}
