package sharedread

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Review m-3: a read that hangs (a hung network share) ends the call
// after the time limit; while it still hangs, further reads of that file
// fail at once instead of piling up; once it ends, reads work again.
func TestAHungReadIsBoundedInTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	start := time.Now()
	err := do(p, 200*time.Millisecond, func(*os.File) error { <-release; return nil })
	if !errors.Is(err, ErrTimeout) || time.Since(start) > time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	start = time.Now()
	if _, _, err := readFile(p, 10, 200*time.Millisecond); !errors.Is(err, ErrTimeout) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("while hung: %v after %v", err, time.Since(start))
	}
	close(release)
	for end := time.Now().Add(2 * time.Second); ; {
		b, _, err := readFile(p, 10, 200*time.Millisecond)
		if err == nil {
			if string(b) != `{}` {
				t.Fatalf("%q", b)
			}
			break
		}
		if time.Now().After(end) {
			t.Fatalf("still refused: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A read that has ended leaves its file free for the next one: when Do
// returns, a second Do on the same file at once is not refused as a
// hung read (macOS CI once failed creds'
// TestAccountFileOverCapExplainsMissingAccount so: the read's goroutine
// sent its result before it freed the file, and was descheduled between
// the two).
func TestBackToBackReadsAreNotRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		if err := Do(p, func(*os.File) error { return nil }); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
}
