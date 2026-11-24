package procs

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CreateNoWindow is the process creation flag that gives a console
// program no console window (design D15, O10).
const CreateNoWindow = 0x08000000

type groupSys struct{ job windows.Handle }

// prepareGroup: no console window (the agent runs without one; a console
// program would get a new, visible one).
func prepareGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: CreateNoWindow}
}

// joinGroup puts the started child into a new job whose processes all end
// when the job is terminated or its last handle closed. What the child
// starts joins the job too. (Between the start and this call the child
// runs for a moment outside the job; codex starts nothing that early.)
func joinGroup(cmd *exec.Cmd) groupSys {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return groupSys{}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return groupSys{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return groupSys{}
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return groupSys{}
	}
	return groupSys{job: job}
}

func (s groupSys) signal(cmd *exec.Cmd, kill bool) {
	if !kill {
		return
	}
	if s.job != 0 {
		_ = windows.TerminateJobObject(s.job, 1)
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s groupSys) release() {
	if s.job != 0 {
		windows.CloseHandle(s.job)
	}
}
