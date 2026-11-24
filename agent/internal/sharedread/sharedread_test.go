package sharedread

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := ReadFile(p, 4)
	if err != nil || string(b) != `{"a"` {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := ReadFile(filepath.Join(t.TempDir(), "none"), 4); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}
