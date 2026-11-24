//go:build linux || darwin

package sharedread

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Review m-3: a FIFO (or any file that is not a regular file) is refused
// at once instead of blocking the open or the read.
func TestFIFOIsRefusedAtOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := ReadFile(p, 1024); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	if _, _, err := ReadFile(t.TempDir(), 1024); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("a folder: %v", err)
	}
}
