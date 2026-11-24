//go:build linux || darwin

package codex

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// Review m-3: an auth.json that is a FIFO does not hold up the engine:
// Login answers at once and the sign-in is not usable.
func TestFIFOAuthFileDoesNotBlock(t *testing.T) {
	home := t.TempDir()
	must(t, syscall.Mkfifo(filepath.Join(home, "auth.json"), 0o600))
	r := NewReader(home, clock.NewFake(t0))
	done := make(chan string, 1)
	go func() { done <- r.Login(context.Background()).State }()
	select {
	case st := <-done:
		eq(t, st, "not_signed_in")
	case <-time.After(3 * time.Second):
		t.Fatal("Login blocked on a FIFO")
	}
}
