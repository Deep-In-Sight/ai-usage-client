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

// The icon is set again when its picture changes, and on Windows also when
// the notification area's icon size changes (a display-scale change), so
// the entry of the new size is loaded within a redraw tick; an unchanged
// picture at an unchanged size is not set again (no NIM_MODIFY every
// minute).
func TestIconIsSetOnPictureOrSizeChange(t *testing.T) {
	a, b := []byte("icon A"), []byte("icon B")
	for _, c := range []struct {
		lastImg  []byte
		lastSize int
		img      []byte
		size     int
		want     bool
	}{
		{nil, 0, a, 16, true}, // first draw
		{a, 16, a, 16, false},
		{a, 16, b, 16, true},
		{a, 16, a, 24, true}, // 100 % -> 150 %
		{a, 24, a, 16, true},
		{a, 0, a, 0, false}, // macOS and Linux: no size
	} {
		if got := needIcon(c.lastImg, c.lastSize, c.img, c.size); got != c.want {
			t.Fatalf("needIcon(%q, %d, %q, %d) = %v", c.lastImg, c.lastSize, c.img, c.size, got)
		}
	}
}
