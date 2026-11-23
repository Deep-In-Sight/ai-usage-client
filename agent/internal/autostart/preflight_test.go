package autostart

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestPreflightLaunchAgent(t *testing.T) {
	la := newLaunchAgent(t, &fakeRun{})
	must(t, la.Preflight(bg))
	entries, _ := os.ReadDir(filepath.Dir(la.PlistPath()))
	eq(t, len(entries), 0) // the probe leaves nothing behind
	readOnlyDir(t, filepath.Dir(la.PlistPath()))
	if err := la.Preflight(bg); err == nil {
		t.Fatal("read-only LaunchAgents accepted")
	}
}

func TestPreflightXDG(t *testing.T) {
	x := &XDGAutostart{Home: t.TempDir()}
	must(t, x.Preflight(bg))
	entries, _ := os.ReadDir(filepath.Dir(x.path()))
	eq(t, len(entries), 0)
	readOnlyDir(t, filepath.Dir(x.path()))
	if err := x.Preflight(bg); err == nil {
		t.Fatal("read-only autostart folder accepted")
	}
}
