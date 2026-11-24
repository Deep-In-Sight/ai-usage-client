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

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/osinfo"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/winapp"
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

	// GOOS is the OS the commands act for ("" means runtime.GOOS).
	GOOS string
	// Context ends a run like SIGTERM (nil means context.Background()).
	Context context.Context
	// TrayAvailable and Tray default to tray.Available and tray.Run.
	TrayAvailable func(ctx context.Context) (bool, string)
	Tray          func(ctx context.Context, c tray.Controller, updates <-chan struct{}, notice string) error
	// Syncer makes the sync client (nil means the real one).
	Syncer func(cfg config.Config, userAgent string) engine.Syncer
	// HasConsole (nil means true) and Alert (nil means none): a Windows GUI
	// executable started from Explorer reports in a message box.
	HasConsole func() bool
	Alert      func(title, text string)
	// WinApp is the "Installed apps" entry (nil means the real one).
	WinApp WinApp
	// Others are the autostart methods For did not pick (nil means none).
	Others func(headless bool) []autostart.Method
}

// autostartOptions are the autostart inputs for this user.
func autostartOptions(d paths.Dirs, headless bool, run autostart.Runner, start func(string, ...string) error) autostart.Options {
	return autostart.Options{Headless: headless, Home: d.Home, DataDir: d.Data, Exe: d.InstallExe, Run: run, Start: start}
}

// WinApp writes and removes the Windows "Installed apps" entry.
type WinApp interface {
	Register(winapp.Entry) error
	Unregister() error
}

func (env Env) hasConsole() bool { return env.HasConsole == nil || env.HasConsole() }

func (env Env) alert(title, text string) {
	if env.Alert != nil {
		env.Alert(title, text)
	}
}

func (env Env) winApp() WinApp {
	if env.WinApp == nil {
		return winAppFuncs{}
	}
	return env.WinApp
}

func (env Env) others(headless bool) []autostart.Method {
	if env.Others == nil {
		return nil
	}
	return env.Others(headless)
}

// tempDir is %TEMP% (or the OS default).
func (env Env) tempDir() string {
	if d := env.Getenv("TEMP"); d != "" {
		return d
	}
	return os.TempDir()
}

func (env Env) goos() string {
	if env.GOOS == "" {
		return runtime.GOOS
	}
	return env.GOOS
}

func (env Env) ctx() context.Context {
	if env.Context == nil {
		return context.Background()
	}
	return env.Context
}

// DefaultEnv is the real environment.
func DefaultEnv() Env {
	args := os.Args[1:]
	// A Windows GUI executable has no console: when started from cmd or
	// PowerShell for a command that prints, write to theirs.
	if runtime.GOOS == "windows" && !winapp.HasConsole() && printsOutput(args) {
		winapp.AttachParentConsole()
	}
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
	opts := func(headless bool) autostart.Options {
		return autostartOptions(dirs, headless, autostart.ExecRunner, procs.StartDetached)
	}
	return Env{
		Args:          args,
		Getenv:        os.Getenv,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		Exe:           exe,
		Dirs:          dirs,
		Run:           autostart.ExecRunner,
		StartDetached: procs.StartDetached,
		Clock:         clock.Real{},
		Autostart:     func(headless bool) (autostart.Method, error) { return autostart.For(opts(headless)) },
		Others:        func(headless bool) []autostart.Method { return autostart.Others(opts(headless)) },
		HasConsole:    winapp.HasConsole,
		Alert:         winapp.Alert,
		GOOS:          runtime.GOOS,
		TrayAvailable: func(ctx context.Context) (bool, string) {
			return tray.Available(ctx, os.Getenv)
		},
		Tray: tray.Run,
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
		word := "no-tray"
		if tray.Compiled {
			word = "tray"
		}
		fmt.Fprintf(env.Stdout, "claude-usage %s %s/%s %s\n", version.Version, runtime.GOOS, runtime.GOARCH, word)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usageText)
		return 0
	}
	fmt.Fprintf(env.Stderr, "claude-usage: unknown command %q\n%s", cmd, usageText)
	return 2
}

// printsOutput: the commands whose output someone reads in a console.
func printsOutput(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "status", "version", "--version", "uninstall", "help", "-h", "--help":
		return true
	}
	return false
}

// noArgs: a downloaded executable next to its config.json installs itself;
// on Windows the installed executable runs (it has no console to show the
// usage in); anything else prints the usage.
func noArgs(env Env) int {
	installed := samePath(env.Exe, env.Dirs.InstallExe)
	if installed && env.goos() == "windows" {
		return runCmd(env, nil)
	}
	adjacent := filepath.Join(filepath.Dir(env.Exe), paths.ConfigFile)
	if _, err := os.Stat(adjacent); err == nil && !installed {
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

// isHeadless: no graphical session (the rule is osinfo.Headless).
func isHeadless(getenv func(string) string) bool { return osinfo.Headless(getenv) }

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
