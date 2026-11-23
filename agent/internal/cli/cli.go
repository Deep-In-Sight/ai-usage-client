// Package cli is the agent's command line: run, install, uninstall, status
// and version.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/clock"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/creds"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/version"
)

// Env is everything the commands touch outside the process, so tests can
// replace it.
type Env struct {
	Args          []string
	Getenv        func(string) string
	Stdout        io.Writer
	Stderr        io.Writer
	Exe           string // this executable
	Dirs          paths.Dirs
	Run           autostart.Runner
	StartDetached func(exe string, args ...string) error
	Clock         clock.Clock
	Autostart     func(headless bool) (autostart.Method, error)
}

// DefaultEnv is the real environment.
func DefaultEnv() Env {
	exe, err := os.Executable()
	if err == nil {
		if r, err2 := filepath.EvalSymlinks(exe); err2 == nil {
			exe = r
		}
	}
	dirs, derr := paths.Current()
	if derr != nil {
		fmt.Fprintln(os.Stderr, "claude-usage:", derr)
	}
	return Env{
		Args:          os.Args[1:],
		Getenv:        os.Getenv,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		Exe:           exe,
		Dirs:          dirs,
		Run:           autostart.ExecRunner,
		StartDetached: procs.StartDetached,
		Clock:         clock.Real{},
		Autostart: func(headless bool) (autostart.Method, error) {
			return autostart.For(headless, dirs.Home, autostart.ExecRunner)
		},
	}
}

const usageText = `usage: claude-usage <command>

  install [--config <config.json>] [--headless]   install for this OS user and start
  uninstall                                       stop and remove everything
  run [--headless]                                run the agent (the service does this)
  status [--json]                                 show what the agent is doing
  version                                         print the version
`

// Main runs one command and returns the exit code.
func Main(env Env) int {
	if env.Dirs.Data == "" {
		fmt.Fprintln(env.Stderr, "claude-usage: no data folder for this user")
		return 1
	}
	if len(env.Args) == 0 {
		return noArgs(env)
	}
	cmd, args := env.Args[0], env.Args[1:]
	switch cmd {
	case "run":
		return runCmd(env, args)
	case "install":
		return installCmd(env, args)
	case "uninstall":
		return uninstallCmd(env, args)
	case "status":
		return statusCmd(env, args)
	case "version", "--version":
		fmt.Fprintf(env.Stdout, "claude-usage %s %s/%s\n", version.Version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usageText)
		return 0
	}
	fmt.Fprintf(env.Stderr, "claude-usage: unknown command %q\n%s", cmd, usageText)
	return 2
}

// noArgs: a downloaded executable next to its config.json installs itself;
// anything else prints the usage.
func noArgs(env Env) int {
	adjacent := filepath.Join(filepath.Dir(env.Exe), paths.ConfigFile)
	if _, err := os.Stat(adjacent); err == nil && !samePath(env.Exe, env.Dirs.InstallExe) {
		return installCmd(env, nil)
	}
	fmt.Fprint(env.Stderr, usageText)
	return 2
}

func samePath(a, b string) bool {
	clean := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return clean(a) == clean(b)
}

// isHeadless: no graphical session. DISPLAY is not looked at on purpose (SSH
// X forwarding sets it on servers).
func isHeadless(getenv func(string) string) bool {
	switch getenv("XDG_SESSION_TYPE") {
	case "x11", "wayland":
		return false
	}
	return getenv("XDG_CURRENT_DESKTOP") == ""
}

// readerOptions builds the sign-in reader options from config.json only;
// the CLAUDE_CONFIG_DIR environment variable is never read here.
func readerOptions(cfg config.Config, d paths.Dirs) creds.Options {
	svc := cfg.KeychainService
	if svc == "" {
		svc = creds.KeychainService(cfg.ClaudeConfigDir)
	}
	return creds.Options{
		Home:            d.Home,
		ClaudeConfigDir: cfg.ClaudeConfigDir,
		KeychainService: svc,
		UseKeychain:     runtime.GOOS == "darwin",
		Keychain:        creds.ReadKeychain,
	}
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func background() context.Context { return context.Background() }
