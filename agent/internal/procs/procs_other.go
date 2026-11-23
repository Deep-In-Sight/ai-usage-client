//go:build !linux && !darwin && !windows

package procs

import (
	"os"
	"os/exec"
)

// FindByExe is not available on this OS.
func FindByExe(string) ([]int, error) { return nil, ErrUnsupported }

// Kill stops the given processes except one.
func Kill(pids []int, except int) {
	for _, pid := range pids {
		if pid == except || pid <= 0 {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

// StartDetached starts exe in the background with no standard streams.
func StartDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
