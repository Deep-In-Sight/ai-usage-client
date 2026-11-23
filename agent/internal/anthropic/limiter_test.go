package anthropic

import (
	"testing"
	"time"
)

var base = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

func TestLimiter(t *testing.T) {
	var l Limiter
	eq(t, l.Allowed(base), true) // nothing recorded yet
	l.Record(Result{FetchedAt: base, HTTPStatus: 200})
	eq(t, l.Allowed(base.Add(59*time.Second)), false)
	eq(t, l.Allowed(base.Add(60*time.Second)), true)

	t1 := base.Add(2 * time.Minute)
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

	t5 := t4.Add(3 * time.Minute)
	l.Record(Result{FetchedAt: t5, HTTPStatus: 0}) // any other result: 60 s
	eq(t, l.NextAllowed(), t5.Add(MinGap))
}

func TestLimiterHourlyCap(t *testing.T) {
	var l Limiter
	at := base
	for i := 0; i < HourlyCap; i++ {
		if !l.Allowed(at) {
			t.Fatalf("attempt %d refused", i)
		}
		l.Record(Result{FetchedAt: at, HTTPStatus: 200})
		at = at.Add(MinGap)
	}
	// 15 attempts in 14 minutes: the 16th waits until the first is an hour old
	eq(t, l.Allowed(at), false)
	eq(t, l.Allowed(base.Add(time.Hour-time.Second)), false)
	eq(t, l.NextAllowed(), base.Add(time.Hour))
	eq(t, l.Allowed(base.Add(time.Hour)), true)
	// any rolling hour holds at most 15
	l.Record(Result{FetchedAt: base.Add(time.Hour), HTTPStatus: 200})
	eq(t, l.Allowed(base.Add(time.Hour+MinGap)), true) // the second one aged out too
	eq(t, l.Allowed(base.Add(time.Hour+MinGap-time.Second)), false)
}

func TestLimiterSnapshotRestore(t *testing.T) {
	var l Limiter
	at := base
	for i := 0; i < HourlyCap; i++ {
		l.Record(Result{FetchedAt: at, HTTPStatus: 429})
		at = at.Add(10 * time.Minute)
	}
	s := l.Snapshot()
	if len(s.Attempts) > HourlyCap {
		t.Fatalf("%d attempts kept", len(s.Attempts))
	}
	var r Limiter
	r.Restore(s)
	eq(t, r.NextAllowed(), l.NextAllowed())
	eq(t, r.Allowed(at), l.Allowed(at))
	// the 429 level survives too
	r.Record(Result{FetchedAt: at.Add(time.Hour), HTTPStatus: 429})
	eq(t, r.NextAllowed(), at.Add(time.Hour+backoffMax))
}

func TestLimiterClockSetBack(t *testing.T) {
	var l Limiter
	l.Record(Result{FetchedAt: base, HTTPStatus: 429})
	l.Record(Result{FetchedAt: base.Add(3 * time.Minute), HTTPStatus: 429})
	// the wall clock goes back a day: the wait is never longer than the
	// longest backoff from now, and old attempts do not block for a day
	back := base.Add(-24 * time.Hour)
	eq(t, l.Allowed(back), false)
	eq(t, l.Allowed(back.Add(backoffMax)), true)
}
