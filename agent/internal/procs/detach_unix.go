//go:build linux || darwin

package procs

import (
	"os"
	"os/exec"
	"syscall"
)

// StartDetached starts exe in its own session with no terminal.
func StartDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
