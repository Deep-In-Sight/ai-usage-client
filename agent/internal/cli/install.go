package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/creds"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/osinfo"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
)

// Exit codes of install (shared with the server's install scripts).
const (
	exitOK         = 0
	exitFailed     = 1
	exitNotLasting = 10 // installed and running, but not after logout/reboot
)

func installCmd(env Env, args []string) int {
	cfgPath := filepath.Join(filepath.Dir(env.Exe), paths.ConfigFile)
	headless := isHeadless(env.Getenv)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--headless":
			headless = true
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(env.Stderr, "claude-usage install: --config needs a path")
				return 2
			}
			i++
			cfgPath = args[i]
		default:
			fmt.Fprintf(env.Stderr, "claude-usage install: unknown argument %q\n", args[i])
			return 2
		}
	}
	ctx := background()

	// 1-3: everything that can fail before anything is changed.
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install: cannot read the config file:", err)
		return exitFailed
	}
	cfg, err := config.Parse(b)
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install:", err)
		return exitFailed
	}
	cfg.ClaudeConfigDir = ""
	if d := env.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		abs, err := filepath.Abs(d)
		if err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install: CLAUDE_CONFIG_DIR:", err)
			return exitFailed
		}
		cfg.ClaudeConfigDir = abs
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install:", err)
		return exitFailed
	}
	method, err := env.Autostart(headless)
	if errors.Is(err, autostart.ErrUnsupported) {
		fmt.Fprintf(env.Stderr, "claude-usage install: autostart on %s is not implemented yet\n", runtime.GOOS)
		return exitFailed
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install:", err)
		return exitFailed
	}
	// Linger first on headless Linux: from "su -" or "sudo -u" there is no
	// user manager yet, and enabling linger is what starts one.
	lingerTried := headless && runtime.GOOS == "linux"
	var linger autostart.Linger
	lingerDetail := ""
	user := osinfo.Current().OSUser
	if lingerTried {
		linger, lingerDetail = autostart.EnsureLinger(ctx, env.Run, user)
	}
	available := method.Available(ctx)

	// 5a: stage the new executable next to its place before stopping anything.
	staged := ""
	if !samePath(env.Exe, env.Dirs.InstallExe) {
		staged, err = stageExe(env.Exe, env.Dirs.InstallExe)
		if err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install: copying the executable:", err)
			return exitFailed
		}
		defer os.Remove(staged) // a no-op once renamed
	}

	// 4: stop what runs now (an upgrade).
	hadUnit := method.State(ctx) != "not installed"
	if err := method.Stop(ctx); err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install: stopping the old service:", err)
	}
	killed := 0
	if pids, err := procs.FindByExe(env.Dirs.InstallExe); err == nil {
		killed = len(pids)
		procs.Kill(pids, os.Getpid())
	}
	// restore starts the agent that was running before, after a failure.
	restore := func() {
		if _, err := os.Stat(env.Dirs.InstallExe); err != nil {
			return
		}
		switch {
		case hadUnit && available:
			if err := method.Register(ctx, env.Dirs.InstallExe); err != nil {
				fmt.Fprintln(env.Stderr, "claude-usage install: restarting the previous agent:", err)
			}
		case killed > 0:
			_ = env.StartDetached(env.Dirs.InstallExe, "run", "--headless")
		}
	}

	// 5b: the executable.
	if staged != "" {
		if err := os.Rename(staged, env.Dirs.InstallExe); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install: replacing the executable:", err)
			restore()
			return exitFailed
		}
	}

	// 6: the config (device id, backlog and state stay as they are).
	cfgDst := env.Dirs.File(paths.ConfigFile)
	if err := config.Save(cfgDst, cfg); err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install: writing the config:", err)
		restore()
		return exitFailed
	}

	// 7: autostart and start.
	code := exitOK
	registered := false
	autostartLine := ""
	if available {
		if err := method.Register(ctx, env.Dirs.InstallExe); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install:", err)
		} else {
			registered = true
			autostartLine = fmt.Sprintf("%s (%s)", method.Name(), method.State(ctx))
		}
	}
	if !registered {
		if err := env.StartDetached(env.Dirs.InstallExe, "run", "--headless"); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install: starting the agent:", err)
			return exitFailed
		}
		fmt.Fprintln(env.Stdout, "autostart could not be registered; the agent runs until you log out or restart")
		autostartLine = "none (started in the background)"
		code = exitNotLasting
	}

	// 8: linger, so the service runs without a login.
	if lingerTried && linger != autostart.LingerEnabled {
		fmt.Fprintf(env.Stdout, "linger is not enabled: the agent runs only while %s is logged in. To keep it running, ask an administrator to run: loginctl enable-linger %s\n", user, user)
		if lingerDetail != "" {
			fmt.Fprintln(env.Stderr, "loginctl:", lingerDetail)
		}
		code = exitNotLasting
	}

	// 9: summary.
	ro := readerOptions(cfg, env.Dirs)
	ro.Clock = env.Clock
	signIn := "not found yet (the agent starts collecting once you sign in)"
	if creds.NewReader(ro).Current(ctx).HasToken {
		signIn = "found"
	}
	fmt.Fprintln(env.Stdout, "installed:", env.Dirs.InstallExe)
	fmt.Fprintln(env.Stdout, "config:", cfgDst)
	fmt.Fprintln(env.Stdout, "autostart:", autostartLine)
	fmt.Fprintln(env.Stdout, "Claude Code sign-in:", signIn)
	return code
}

// stageExe copies src to a temporary file in dst's folder (mode 0755) and
// returns its path; the caller renames it into place.
func stageExe(src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".new-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}
