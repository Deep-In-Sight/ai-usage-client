package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCodexBin is tools/fakecodex built once for the package's tests.
var fakeCodexBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakecodex")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fakeCodexBin, err = buildFakeCodex(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "building fakecodex:", err)
		os.RemoveAll(dir)
		os.Exit(2)
	}
	AllowCLIRuns(true) // these tests run fakecodex; TestNoCodexRunsUnlessAllowed switches it off
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func buildFakeCodex(dir string) (string, error) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		if goBin, err = exec.LookPath("go"); err != nil {
			return "", err
		}
	}
	out := filepath.Join(dir, "codex")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command(goBin, "build", "-o", out, "../../tools/fakecodex")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, b)
	}
	return out, nil
}

// fakeCodex puts a copy of fakecodex in its own folder with these flags
// (its .args file) and returns its path.
func fakeCodex(t *testing.T, flags ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, filepath.Base(fakeCodexBin))
	if err := os.Link(fakeCodexBin, p); err != nil {
		b, err := os.ReadFile(fakeCodexBin)
		must(t, err)
		must(t, os.WriteFile(p, b, 0o755))
	}
	must(t, os.WriteFile(p+".args", []byte(strings.Join(flags, "\n")+"\n"), 0o600))
	return p
}

// pidsIn reads the process ids fakecodex wrote, waiting up to 5 s for them.
func pidsIn(t *testing.T, path string) []int {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; {
		b, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(b), "\n") {
			var out []int
			for _, f := range strings.Fields(string(b)) {
				n, err := strconv.Atoi(f)
				must(t, err)
				out = append(out, n)
			}
			return out
		}
		if time.Now().After(end) {
			t.Fatalf("no pid file %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// noneAlive fails the test if one of pids is still a running process
// (waiting up to 2 s: a killed grandchild is reaped by its new parent).
func noneAlive(t *testing.T, pids []int) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); ; {
		var left []int
		for _, p := range pids {
			if processAlive(p) {
				left = append(left, p)
			}
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("processes still running: %v", left)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
