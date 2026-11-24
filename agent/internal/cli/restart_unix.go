//go:build linux || darwin

package cli

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// restartSelf replaces this process with a new run of the same executable
// (the same pid, the same file even if it was replaced on disk on Linux),
// with restartsEnv set to n.
func restartSelf(args []string, n int) error {
	exe := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		p, err := os.Executable()
		if err != nil {
			return err
		}
		exe = p
	}
	environ := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, restartsEnv+"=") {
			environ = append(environ, kv)
		}
	}
	environ = append(environ, restartsEnv+"="+strconv.Itoa(n))
	return syscall.Exec(exe, append([]string{os.Args[0]}, args...), environ)
}
