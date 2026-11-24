package tray

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/clock"
)

var desktop = func(k string) string {
	return map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}[k]
}

type availResult struct {
	ok     bool
	reason string
}

// runAvailable runs availableWith and moves the fake clock forward in 2 s
// steps until it answers; it returns the answer and the fake time used.
func runAvailable(t *testing.T, getenv func(string) string, hasOwner func() (bool, error)) (availResult, time.Duration) {
	t.Helper()
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	done := make(chan availResult, 1)
	go func() {
		ok, reason := availableWith(context.Background(), getenv, hasOwner, clk)
		done <- availResult{ok, reason}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case r := <-done:
			return r, clk.Now().Sub(start)
		case <-time.After(2 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("availableWith did not return")
		}
		clk.Advance(500 * time.Millisecond)
	}
}

func TestAvailableNoWatcher(t *testing.T) {
	var calls atomic.Int32
	r, used := runAvailable(t, desktop, func() (bool, error) { calls.Add(1); return false, nil })
	if r.ok || r.reason != "no StatusNotifierWatcher" {
		t.Fatalf("%+v", r)
	}
	if used < WatcherWait || used > WatcherWait+5*time.Second {
		t.Fatalf("gave up after %v of fake time", used)
	}
	if n := calls.Load(); n < 25 || n > 35 {
		t.Fatalf("%d checks, want one every 2 s", n)
	}
}

func TestAvailableWatcherAppearsLate(t *testing.T) {
	answers := []bool{false, false, true}
	var i atomic.Int32
	r, _ := runAvailable(t, desktop, func() (bool, error) {
		n := int(i.Add(1)) - 1
		return answers[min(n, len(answers)-1)], nil
	})
	if !r.ok || r.reason != "" {
		t.Fatalf("%+v", r)
	}
	eq(t, int(i.Load()), 3)
}

func TestAvailableHeadlessEnv(t *testing.T) {
	r, _ := runAvailable(t, func(string) string { return "" }, func() (bool, error) { t.Fatal("hasOwner called"); return false, nil })
	if r.ok || r.reason != "no desktop session" {
		t.Fatalf("%+v", r)
	}
}

func TestAvailableNoSessionBus(t *testing.T) {
	r, _ := runAvailable(t, desktop, func() (bool, error) { return false, errNoBus })
	if r.ok || r.reason != "no session bus" {
		t.Fatalf("%+v", r)
	}
}
