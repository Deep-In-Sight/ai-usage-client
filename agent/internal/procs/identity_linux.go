package procs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// IdentityOf reads the start time of pid (clock ticks since boot, field 22
// of /proc/<pid>/stat). A zombie counts as gone.
func IdentityOf(pid int) (Identity, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return Identity{}, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the name may hold spaces and ')'
	if i < 0 || i+2 > len(s) {
		return Identity{}, errors.New("procs: unreadable /proc stat")
	}
	f := strings.Fields(s[i+2:]) // from field 3 (state) on
	if len(f) < 20 {
		return Identity{}, errors.New("procs: short /proc stat")
	}
	if f[0] == "Z" || f[0] == "X" {
		return Identity{}, os.ErrNotExist
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, Start: start}, nil
}
