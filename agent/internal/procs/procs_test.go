package procs

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// helpers are more helper modes (by AI_USAGE_HELPER), added by OS-specific
// test files.
var helpers = map[string]func(){}

// TestMain lets the test binary act as a long-running helper process.
func TestMain(m *testing.M) {
	if os.Getenv("AI_USAGE_HELPER") == "sleep" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	if f := helpers[os.Getenv("AI_USAGE_HELPER")]; f != nil {
		f()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFindByExeFindsChild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "helper-copy"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cp := filepath.Join(t.TempDir(), name)
	copyFile(t, self, cp)

	cmd := exec.Command(cp)
	cmd.Env = append(os.Environ(), "AI_USAGE_HELPER=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(pids, cmd.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d not in %v", cmd.Process.Pid, pids)
		}
		pids, err = FindByExe(cp)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runtime.GOOS == "linux" {
		// the executable replaced on disk (an upgrade) is still found
		if err := os.Remove(cp); err != nil {
			t.Fatal(err)
		}
		pids, _ = FindByExe(cp)
		if !slices.Contains(pids, cmd.Process.Pid) {
			t.Fatalf("deleted exe: pid %d not in %v", cmd.Process.Pid, pids)
		}
	}
	// except leaves a pid alone
	Kill(pids, cmd.Process.Pid)
	select {
	case <-done:
		t.Fatal("excepted process was killed")
	case <-time.After(200 * time.Millisecond):
	}
	start := time.Now()
	Kill(pids, 0)
	select {
	case <-done:
		done <- nil // for the cleanup
	case <-time.After(6*time.Second - time.Since(start)):
		t.Fatal("process did not exit within 6 s")
	}
	if pids, _ := FindByExe(filepath.Join(t.TempDir(), "nothing")); len(pids) != 0 {
		t.Fatalf("%v", pids)
	}
}

func TestLockExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "run.lock")
	unlock, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	unlock2, err := Lock(p)
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	unlock2()
}
