//go:build linux || darwin

package procs

import (
	"os/exec"
	"syscall"
)

type groupSys struct{ pgid int }

// prepareGroup: the child leads a new process group, so that a signal to
// the group reaches everything it starts (unless that leaves the group).
func prepareGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func joinGroup(cmd *exec.Cmd) groupSys { return groupSys{pgid: cmd.Process.Pid} }

func (s groupSys) signal(_ *exec.Cmd, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	if s.pgid > 0 {
		_ = syscall.Kill(-s.pgid, sig)
	}
}

func (groupSys) release() {}
