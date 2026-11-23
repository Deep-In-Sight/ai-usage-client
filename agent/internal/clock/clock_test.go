package clock

import (
	"testing"
	"time"
)

func TestFakeAfterFiresOnAdvance(t *testing.T) {
	f := NewFake(time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC))
	ch := f.After(60 * time.Second)
	f.Advance(59 * time.Second)
	select {
	case <-ch:
		t.Fatal("fired early")
	default:
	}
	f.Advance(time.Second)
	select {
	case <-ch:
	default:
		t.Fatal("did not fire")
	}
}

func TestFakeSetAndZeroAfter(t *testing.T) {
	t0 := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	f := NewFake(t0)
	select {
	case <-f.After(0):
	default:
		t.Fatal("After(0) should fire at once")
	}
	ch := f.After(time.Hour)
	f.Set(t0.Add(2 * time.Hour))
	select {
	case <-ch:
	default:
		t.Fatal("Set past the deadline did not fire")
	}
	if !f.Now().Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("Now = %v", f.Now())
	}
}
