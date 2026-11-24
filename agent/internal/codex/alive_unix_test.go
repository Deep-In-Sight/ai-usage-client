//go:build linux || darwin

package codex

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processAlive: pid runs (on Linux a zombie counts as ended: in a
// container nobody may reap an orphan).
func processAlive(pid int) bool {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i >= 0 && len(s) > i+2 {
			return s[i+2] != 'Z'
		}
	}
	return syscall.Kill(pid, 0) == nil
}
