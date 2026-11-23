package procs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const sZomb = 5 // SZOMB in <sys/proc.h>

func cleanAbs(p string) string {
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// FindByExe lists this user's processes whose executable is exe: the path
// each was started from (kern.procargs2), compared with and without
// symbolic links (/var is /private/var).
func FindByExe(exe string) ([]int, error) {
	want := cleanAbs(exe)
	alt := resolved(want)
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, kp := range kps {
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		b, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil || len(b) < 5 {
			continue
		}
		rest := b[4:] // argc, then the executable path
		i := bytes.IndexByte(rest, 0)
		if i <= 0 {
			continue
		}
		p := cleanAbs(string(rest[:i]))
		if p == want || p == alt || resolved(p) == alt {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func gone(pid int) bool {
	if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
		return true
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return true
	}
	return kp.Proc.P_pid != int32(pid) || kp.Proc.P_stat == sZomb
}

// Kill stops the given processes except one (usually the caller): SIGTERM,
// then SIGKILL after 5 s.
func Kill(pids []int, except int) {
	var left []int
	for _, pid := range pids {
		if pid == except || pid <= 0 {
			continue
		}
		if syscall.Kill(pid, syscall.SIGTERM) == nil {
			left = append(left, pid)
		}
	}
	deadline := time.Now().Add(killWait * time.Second)
	for len(left) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		alive := left[:0]
		for _, pid := range left {
			if !gone(pid) {
				alive = append(alive, pid)
			}
		}
		left = alive
	}
	for _, pid := range left {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
