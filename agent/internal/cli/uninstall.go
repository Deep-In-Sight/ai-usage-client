package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
)

func uninstallCmd(env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(env.Stderr, "claude-usage uninstall: unknown argument %q\n", args[0])
		return 2
	}
	ctx := background()
	method, err := env.Autostart(true)
	switch {
	case err == nil:
		if err := method.Stop(ctx); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
		}
		if err := method.Unregister(ctx); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
		}
	case !errors.Is(err, autostart.ErrUnsupported):
		fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
	}
	if pids, err := procs.FindByExe(env.Dirs.InstallExe); err == nil {
		procs.Kill(pids, os.Getpid())
	}
	failed := false
	if err := os.Remove(env.Dirs.InstallExe); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
		failed = true
	}
	// Only ever remove our own folder.
	switch filepath.Base(env.Dirs.Data) {
	case "claude-usage", "ClaudeUsage":
		if err := os.RemoveAll(env.Dirs.Data); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
			failed = true
		}
	default:
		fmt.Fprintln(env.Stderr, "claude-usage uninstall: unexpected data folder, left in place:", env.Dirs.Data)
		failed = true
	}
	if failed {
		return 1
	}
	fmt.Fprintln(env.Stdout, "removed")
	return 0
}
