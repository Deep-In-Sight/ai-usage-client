package tray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIconFilesAreBoundedAndOurs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tray-icons")
	a1, err := iconFile(dir, []byte("icon A"), 3)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := iconFile(dir, []byte("icon A"), 3)
	eq(t, a1, a2) // the same image is the same file
	b, _ := iconFile(dir, []byte("icon B"), 3)
	if b == a1 {
		t.Fatal("different images share a file")
	}
	// someone else's file in the folder is never touched
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var last string
	for _, s := range []string{"C", "D", "E", "F"} {
		last, _ = iconFile(dir, []byte("icon "+s), 3)
	}
	ours, _ := filepath.Glob(filepath.Join(dir, "*.ico"))
	eq(t, len(ours), 3)
	if _, err := os.Stat(last); err != nil {
		t.Fatal("the icon just written was removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a file that is not ours was removed")
	}
	got, _ := os.ReadFile(last)
	eq(t, string(got), "icon F")

	// on exit only our files go; the folder goes when it is then empty
	removeIconFiles(dir)
	ours, _ = filepath.Glob(filepath.Join(dir, "*.ico"))
	eq(t, len(ours), 0)
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a file that is not ours was removed")
	}
	os.Remove(other)
	removeIconFiles(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("empty folder left")
	}
}
