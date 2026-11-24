package procs

import (
	"os"

	"golang.org/x/sys/unix"
)

// IdentityOf reads the start time of pid (microseconds since the epoch,
// from kern.proc.pid). A zombie counts as gone.
func IdentityOf(pid int) (Identity, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Identity{}, err
	}
	if int(kp.Proc.P_pid) != pid || kp.Proc.P_stat == sZomb {
		return Identity{}, os.ErrNotExist
	}
	t := kp.Proc.P_starttime
	return Identity{PID: pid, Start: uint64(t.Sec)*1e6 + uint64(t.Usec)}, nil
}
