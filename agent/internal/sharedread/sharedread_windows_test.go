package sharedread

import (
	"os"
	"path/filepath"
	"testing"
)

// Review I-1: while the agent holds auth.json open, Codex saving its
// sign-in (a new file renamed over auth.json, as std::fs::rename does:
// MoveFileEx with MOVEFILE_REPLACE_EXISTING, the same as os.Rename) must
// succeed. The control shows the failure a plain os.Open caused.
func TestRenameOverAnOpenFileSucceeds(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(auth, `{"old":1}`)

	// control: os.Open (FILE_SHARE_READ|FILE_SHARE_WRITE only)
	f, err := os.Open(auth)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "tmp1"), `{"new":1}`)
	controlErr := os.Rename(filepath.Join(dir, "tmp1"), auth)
	f.Close()
	if controlErr == nil {
		t.Fatal("control: the rename over a file held by os.Open succeeded, so this test cannot tell the difference")
	}
	t.Logf("control: rename over a file held by os.Open: %v", controlErr)

	// ours
	g, err := Open(auth)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "tmp2"), `{"new":2}`)
	if err := os.Rename(filepath.Join(dir, "tmp2"), auth); err != nil {
		g.Close()
		t.Fatalf("rename over a file held by sharedread.Open: %v", err)
	}
	// the open handle still reads the old content; the path has the new
	b := make([]byte, 64)
	n, _ := g.Read(b)
	g.Close()
	if string(b[:n]) != `{"old":1}` {
		t.Fatalf("held handle read %q", b[:n])
	}
	now, err := os.ReadFile(auth)
	if err != nil || string(now) != `{"new":2}` {
		t.Fatalf("after: %q %v", now, err)
	}
	// and a delete while open
	h, err := Open(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := os.Remove(auth); err != nil {
		t.Fatalf("delete while held: %v", err)
	}
}
