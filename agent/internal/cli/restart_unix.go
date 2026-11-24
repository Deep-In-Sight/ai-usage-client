//go:build linux || darwin

package cli

import (
	"os"
	"runtime"
	"syscall"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
)

// restartSelf replaces this process with a new run of the same executable
// (the same pid, the same file even if it was replaced on disk on Linux),
// with restartsEnv set to n and, when the old session's bus is gone, the
// runtime folder's bus (restartEnviron).
func restartSelf(args []string, n int) error {
	exe := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		p, err := os.Executable()
		if err != nil {
			return err
		}
		exe = p
	}
	return syscall.Exec(exe, append([]string{os.Args[0]}, args...), restartEnviron(os.Environ(), n, tray.BusReachable))
}
