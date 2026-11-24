package procs

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestKillChecksTheImageAgain: a pid whose process is no longer the one
// FindByExe saw (pid reuse) is left alone.
func TestKillChecksTheImageAgain(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "helper-copy.exe")
	copyFile(t, self, cp)
	cmd := exec.Command(cp)
	cmd.Env = append(os.Environ(), "AI_USAGE_HELPER=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, _ := FindByExe(cp)
		if slices.Contains(pids, pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not found")
		}
		time.Sleep(20 * time.Millisecond)
	}
	seen.Store(pid, `C:\somewhere\else.exe`) // as if the pid now belonged to another program
	Kill([]int{pid}, 0)
	select {
	case <-done:
		t.Fatal("killed a process whose image changed")
	case <-time.After(300 * time.Millisecond):
	}
	_, _ = FindByExe(cp)
	Kill([]int{pid}, 0)
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("not killed")
	}
}
