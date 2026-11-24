package sharedread

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Review I-1: the packages that read other programs' files (Claude Code's
// sign-in and account file, Codex's auth.json, config.toml and npm
// shims) open them only through this package.
func TestOtherProgramsFilesAreOpenedShared(t *testing.T) {
	direct := regexp.MustCompile(`\bos\.(Open|OpenFile|ReadFile)\(`)
	for _, dir := range []string{"../creds", "../codex"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				if direct.MatchString(line) {
					t.Errorf("%s:%d opens a file directly: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
