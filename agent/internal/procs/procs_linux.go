package procs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// FindByExe lists the processes (of any user it can see) whose executable
// is exe, including ones whose file was replaced since they started.
func FindByExe(exe string) ([]int, error) {
	want := filepath.Clean(exe)
	if abs, err := filepath.Abs(want); err == nil {
		want = abs
	}
	alt := want
	if r, err := filepath.EvalSymlinks(want); err == nil {
		alt = r
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		link, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		link = strings.TrimSuffix(link, " (deleted)")
		if link == want || link == alt {
			pids = append(pids, pid)
		}
	}
	return pids, nil
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
	deadline := time.Now().Add(5 * time.Second)
	for len(left) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		alive := left[:0]
		for _, pid := range left {
			if gone(pid) {
				continue
			}
			alive = append(alive, pid)
		}
		left = alive
	}
	for _, pid := range left {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// gone: the process no longer exists or is a zombie.
func gone(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] == 'Z'
	}
	return false
}
