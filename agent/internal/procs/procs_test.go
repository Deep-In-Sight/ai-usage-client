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

func TestFindByExeFindsChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		_, err := FindByExe("/x")
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%v", err)
		}
		t.Skip("linux only")
	}
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	cp := filepath.Join(t.TempDir(), "sleep-copy")
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(cp, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(out, in)
	in.Close()
	out.Close()

	cmd := exec.Command(cp, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	pids, err := FindByExe(cp)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pids, cmd.Process.Pid) {
		t.Fatalf("pid %d not in %v", cmd.Process.Pid, pids)
	}
	// the executable replaced on disk (an upgrade) is still found
	if err := os.Remove(cp); err != nil {
		t.Fatal(err)
	}
	pids, _ = FindByExe(cp)
	if !slices.Contains(pids, cmd.Process.Pid) {
		t.Fatalf("deleted exe: pid %d not in %v", cmd.Process.Pid, pids)
	}
	// except leaves a pid alone
	Kill(pids, cmd.Process.Pid)
	select {
	case <-done:
		t.Fatal("excepted process was killed")
	case <-time.After(200 * time.Millisecond):
	}
	Kill(pids, 0)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit")
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
