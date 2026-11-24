package procs

import "os/exec"

// Group is a started child process together with every process it starts:
// on Unix its own process group, on Windows a job object (and no console
// window). The agent uses it for the Codex CLI it runs, so that ending the
// child ends whatever the child started too.
type Group struct {
	Cmd *exec.Cmd
	sys groupSys
}

// StartGroup starts cmd as a Group. cmd must not have been started; its
// SysProcAttr is replaced.
func StartGroup(cmd *exec.Cmd) (*Group, error) {
	g := &Group{Cmd: cmd}
	prepareGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	g.sys = joinGroup(cmd)
	return g, nil
}

// Terminate asks every process of the group to end (Unix: SIGTERM; on
// Windows there is no gentle way and it does nothing).
func (g *Group) Terminate() { g.sys.signal(g.Cmd, false) }

// Kill ends every process of the group now (Unix: SIGKILL to the group;
// Windows: the job is terminated). Safe after the child has ended: what
// it left behind is ended too.
func (g *Group) Kill() { g.sys.signal(g.Cmd, true) }

// Release frees what the group holds (Windows: the job, which also ends
// any process still in it). Call it once, after Cmd.Wait.
func (g *Group) Release() {
	g.sys.release()
	g.sys = groupSys{}
}
