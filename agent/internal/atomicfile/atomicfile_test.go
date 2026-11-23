package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	p := filepath.Join(dir, "f.json")
	for _, c := range []string{"one", "two"} {
		if err := Write(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(p)
		if string(b) != c {
			t.Fatalf("got %q", b)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("%d entries", len(ents))
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Fatal(st.Mode())
		}
	}
}
