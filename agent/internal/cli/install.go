package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/osinfo"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/version"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/winapp"
)

// Exit codes of install (shared with the server's install scripts).
const (
	exitOK         = 0
	exitFailed     = 1
	exitNotLasting = 10 // installed and running, but not after logout/reboot
)

// installCmd installs, then, without a console (a Windows executable
// double-clicked next to its config.json), says how it went in a message box.
func installCmd(env Env, args []string) int {
	code, problem := install(env, args)
	if !env.hasConsole() {
		if code == exitOK || code == exitNotLasting {
			env.alert(text.App, text.AlertInstalled)
		} else {
			env.alert(text.App, text.AlertInstallFailed+problem)
		}
	}
	return code
}

// install returns the exit code and, on failure, the first error line.
func install(env Env, args []string) (int, string) {
	goos := env.goos()
	failWith := func(code int, msg string) (int, string) {
		fmt.Fprintln(env.Stderr, "claude-usage install: "+msg)
		return code, firstLine(msg)
	}
	fail := func(msg string) (int, string) { return failWith(exitFailed, msg) }

	cfgPath := filepath.Join(filepath.Dir(env.Exe), paths.ConfigFile)
	headless := goos == "linux" && isHeadless(env.Getenv)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--headless":
			headless = true
		case "--config":
			if i+1 >= len(args) {
				return failWith(2, "--config needs a path")
			}
			i++
			cfgPath = args[i]
		default:
			return failWith(2, fmt.Sprintf("unknown argument %q", args[i]))
		}
	}
	runArgs := []string{"run"}
	if headless {
		runArgs = append(runArgs, "--headless")
	}
	ctx := background()

	// 1-3: everything that can fail before anything is changed.
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return fail("cannot read the config file: " + err.Error())
	}
	cfg, err := config.Parse(b)
	if err != nil {
		return fail(err.Error())
	}
	cfg.ClaudeConfigDir = ""
	keptDir := false
	if d := env.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		abs, err := filepath.Abs(d)
		if err != nil {
			return fail("CLAUDE_CONFIG_DIR: " + err.Error())
		}
		cfg.ClaudeConfigDir = abs
	} else if old, err := config.Load(env.Dirs.File(paths.ConfigFile)); err == nil && old.ClaudeConfigDir != "" {
		// An upgrade from a shell without the variable keeps the folder the
		// agent was installed with.
		cfg.ClaudeConfigDir, keptDir = old.ClaudeConfigDir, true
	}
	if err := cfg.Validate(); err != nil {
		return fail(err.Error())
	}
	method, err := env.Autostart(headless)
	if errors.Is(err, autostart.ErrUnsupported) {
		return fail("autostart on " + goos + " is not supported")
	}
	if err != nil {
		return fail(err.Error())
	}
	others := env.others(headless)
	// Linger first on headless Linux: from "su -" or "sudo -u" there is no
	// user manager yet, and enabling linger is what starts one.
	lingerTried := headless && goos == "linux"
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
			return fail("copying the executable: " + err.Error())
		}
		defer os.Remove(staged) // a no-op once renamed
	}

	// 4: stop what runs now (an upgrade), by every method and by path. First
	// make sure the autostart entry can be written: if not, an upgrade leaves
	// the installed agent alone, and a fresh install runs without one.
	st := method.State(ctx)
	hadEntry := st != "not installed" && st != "not registered"
	wasRunning := false
	if pids, err := procs.FindByExe(env.Dirs.InstallExe); err == nil {
		wasRunning = slices.ContainsFunc(pids, func(p int) bool { return p != os.Getpid() })
	}
	var preflightErr error
	if p, ok := method.(autostart.Preflighter); ok && available {
		if preflightErr = p.Preflight(ctx); preflightErr != nil {
			if _, err := os.Stat(env.Dirs.InstallExe); err == nil && staged != "" {
				return fail(fmt.Sprintf("start at login cannot be set up (%v); the installed agent was left as it was", preflightErr))
			}
			available = false
		}
	}
	for _, m := range append([]autostart.Method{method}, others...) {
		if err := m.Stop(ctx); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install: stopping the old agent:", err)
		}
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
		if hadEntry && available {
			err := method.Register(ctx, env.Dirs.InstallExe)
			if err == nil {
				return
			}
			fmt.Fprintln(env.Stderr, "claude-usage install: restarting the previous agent:", err)
		}
		if hadEntry || wasRunning || killed > 0 {
			if err := env.StartDetached(env.Dirs.InstallExe, runArgs...); err != nil {
				fmt.Fprintln(env.Stderr, "claude-usage install: starting the previous agent:", err)
			}
		}
	}

	// 5b: the executable.
	if staged != "" {
		if err := replaceFile(goos, staged, env.Dirs.InstallExe, os.Rename, 300*time.Millisecond); err != nil {
			restore()
			return fail("replacing the executable: " + err.Error())
		}
	}

	// 6: the config (device id, backlog and state stay as they are).
	cfgDst := env.Dirs.File(paths.ConfigFile)
	if err := config.Save(cfgDst, cfg); err != nil {
		restore()
		return fail("writing the config: " + err.Error())
	}
	// The "Installed apps" entry, and the notice the next tray run shows once.
	if goos == "windows" {
		sweepRemoved(env)
		var size int64
		if fi, err := os.Stat(env.Dirs.InstallExe); err == nil {
			size = fi.Size()
		}
		if err := env.winApp().Register(winapp.EntryFor(env.Dirs.InstallExe, version.Version, size, env.Clock.Now())); err != nil {
			fmt.Fprintln(env.Stderr, "claude-usage install: Installed apps entry:", err)
		}
	}
	if err := os.WriteFile(env.Dirs.File(paths.NotifyFile), []byte(version.Version), 0o600); err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage install: notice marker:", err)
	}

	// 7: autostart and start.
	code := exitOK
	registered := false
	autostartLine := ""
	autostartProblem := preflightErr
	if available {
		err := method.Register(ctx, env.Dirs.InstallExe)
		switch {
		case err == nil:
			registered = true
			autostartLine = fmt.Sprintf("%s (%s)", method.Name(), method.State(ctx))
		case errors.Is(err, autostart.ErrStartFailed):
			// The entry is there for the next login; start the agent now.
			fmt.Fprintln(env.Stderr, "claude-usage install:", err)
			if err := env.StartDetached(env.Dirs.InstallExe, runArgs...); err != nil {
				return fail("starting the agent: " + err.Error())
			}
			registered = true
			autostartLine = method.Name() + " (registered; started directly)"
		default:
			// The agent still runs, until logout (exit 10).
			autostartProblem = err
		}
	}
	if registered {
		// "Start at login" turned off in the tray stays off after an upgrade
		// (the agent still runs now).
		if _, err := os.Stat(env.Dirs.File(paths.AutostartOffFile)); err == nil {
			if t, ok := method.(autostart.Toggler); ok && t.SetEnabled(ctx, env.Dirs.InstallExe, false) == nil {
				fmt.Fprintln(env.Stdout, text.AutostartStaysOff)
				autostartLine = method.Name() + " (off)"
			}
		}
		for _, o := range others {
			if err := o.Unregister(ctx); err != nil {
				fmt.Fprintln(env.Stderr, "claude-usage install: removing", o.Name()+":", err)
			}
		}
	} else {
		if err := env.StartDetached(env.Dirs.InstallExe, runArgs...); err != nil {
			return fail("starting the agent: " + err.Error())
		}
		if autostartProblem != nil {
			fmt.Fprintf(env.Stdout, text.AutostartFailed+"\n", autostartProblem)
		} else {
			fmt.Fprintln(env.Stdout, text.NotLasting)
		}
		autostartLine = "none (started in the background)"
		code = exitNotLasting
	}

	// 8: linger, so the service runs without a login.
	if lingerTried && linger != autostart.LingerEnabled {
		fmt.Fprintf(env.Stdout, text.LingerOff+"\n", user, user)
		if lingerDetail != "" {
			fmt.Fprintln(env.Stderr, "loginctl:", lingerDetail)
		}
		code = exitNotLasting
	}

	// 9: summary.
	ro := readerOptions(cfg, env.Dirs)
	ro.Clock = env.Clock
	signIn := text.SignInNotFound
	if creds.NewReader(ro).Current(ctx).HasToken {
		signIn = text.SignInFound
	}
	fmt.Fprintln(env.Stdout, "installed:", env.Dirs.InstallExe)
	fmt.Fprintln(env.Stdout, "config:", cfgDst)
	fmt.Fprintln(env.Stdout, "autostart:", autostartLine)
	fmt.Fprintln(env.Stdout, "Claude Code sign-in:", signIn)
	if cfg.ClaudeConfigDir != "" {
		line := "Claude Code folder: " + cfg.ClaudeConfigDir
		if keptDir {
			line += " " + text.KeptClaudeDir
		}
		fmt.Fprintln(env.Stdout, line)
	}
	if line := text.Installed(goos, headless); line != "" {
		fmt.Fprintln(env.Stdout, line)
	}
	if goos == "linux" {
		ext := filepath.Join(env.Dirs.Home, ".local", "share", "gnome-shell", "extensions", "claude-usage@localhost")
		if _, err := os.Stat(ext); err == nil {
			fmt.Fprintln(env.Stdout, text.GnomeExtensionNote)
		}
	}
	return code, ""
}

// replaceFile renames src over dst. On Windows the old executable can stay
// locked for a moment after its process ended, so it tries for about 3 s.
func replaceFile(goos, src, dst string, rename func(string, string) error, wait time.Duration) error {
	tries := 1
	if goos == "windows" {
		tries = 10
	}
	var err error
	for i := 0; i < tries; i++ {
		if err = rename(src, dst); err == nil {
			return nil
		}
		time.Sleep(wait)
	}
	return err
}

// firstLine is s up to its first line break.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
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
