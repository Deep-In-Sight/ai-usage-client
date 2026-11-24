//go:build linux || darwin

package creds

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Review m-3: a credentials file that is a FIFO does not hold up the
// engine: the read answers at once with no sign-in.
func TestFIFOCredentialsDoNotBlock(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(home, ".claude", ".credentials.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReader(Options{Home: home})
	done := make(chan Login, 1)
	go func() { done <- r.Current(context.Background()) }()
	select {
	case l := <-done:
		if l.HasToken {
			t.Fatalf("%+v", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reading the sign-in blocked on a FIFO")
	}
}
