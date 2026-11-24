//go:build !linux && !darwin && !windows

package procs

import "os/exec"

type groupSys struct{}

func prepareGroup(*exec.Cmd) {}

func joinGroup(*exec.Cmd) groupSys { return groupSys{} }

func (groupSys) signal(cmd *exec.Cmd, kill bool) {
	if kill && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (groupSys) release() {}
