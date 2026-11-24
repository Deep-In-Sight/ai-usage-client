package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/version"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/winapp"
)

// removalSteps is the order in which everything is removed: by "uninstall"
// (another process) or by the tray's Remove (inside the running agent,
// which must first stop its engine and let go of run.lock).
func removalSteps(goos string, inProcess bool) []string {
	switch {
	case goos == "windows" && !inProcess:
		return []string{"stop", "kill", "unregister-autostart", "unregister-app", "remove-exe", "remove-install-dir", "remove-data"}
	case goos == "windows":
		return []string{"stop-engine", "unlock", "unregister-autostart", "unregister-app", "remove-exe", "remove-install-dir", "remove-data"}
	case goos == "darwin" && !inProcess:
		return []string{"stop", "kill", "unregister-autostart", "remove-data"}
	case goos == "darwin":
		// bootout ends this process, so it comes last.
		return []string{"stop-engine", "unlock", "remove-plist", "remove-data", "bootout-self"}
	case !inProcess:
		return []string{"stop", "kill", "unregister-autostart", "remove-exe", "remove-data"}
	}
	return []string{"stop-engine", "unlock", "unregister-autostart", "remove-exe", "remove-data"}
}

// remover carries out removal steps. Linger is never touched.
type remover struct {
	env        Env
	methods    []autostart.Method
	stopEngine func()
	unlock     func()
}

func newRemover(env Env) *remover {
	r := &remover{env: env}
	if m, err := env.Autostart(true); err == nil {
		r.methods = append(r.methods, m)
	}
	r.methods = append(r.methods, env.others(true)...)
	return r
}

// run does every step, going on after a failure, and returns the failures.
func (r *remover) run(steps []string) error {
	var errs []error
	for _, s := range steps {
		if err := r.step(s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s, err))
		}
	}
	return errors.Join(errs...)
}

func (r *remover) step(name string) error {
	ctx := context.Background()
	env := r.env
	switch name {
	case "stop":
		for _, m := range r.methods {
			if err := m.Stop(ctx); err != nil {
				fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
			}
		}
	case "kill":
		if pids, err := procs.FindByExe(env.Dirs.InstallExe); err == nil {
			procs.Kill(pids, os.Getpid())
		}
	case "unregister-autostart":
		var errs []error
		for _, m := range r.methods {
			errs = append(errs, m.Unregister(ctx))
		}
		return errors.Join(errs...)
	case "unregister-app":
		return env.winApp().Unregister()
	case "remove-exe":
		exe := env.Dirs.InstallExe
		if env.goos() == "windows" && samePath(env.Exe, exe) {
			// A running exe cannot be deleted, but it can be moved aside.
			dst := filepath.Join(env.tempDir(), fmt.Sprintf("claude-usage-removed-%d.exe", os.Getpid()))
			if err := os.Rename(exe, dst); err != nil {
				fmt.Fprintf(env.Stdout, text.CannotRemoveRunning+"\n", exe)
				// Keep it findable: put the "Installed apps" entry back.
				var size int64
				if fi, err := os.Stat(exe); err == nil {
					size = fi.Size()
				}
				_ = env.winApp().Register(winapp.EntryFor(exe, version.Version, size, env.Clock.Now()))
			}
			return nil
		}
		if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case "remove-install-dir":
		_ = os.Remove(filepath.Dir(env.Dirs.InstallExe)) // only when empty
	case "remove-data":
		// Only ever remove our own folder.
		switch filepath.Base(env.Dirs.Data) {
		case "claude-usage", "ClaudeUsage":
			return os.RemoveAll(env.Dirs.Data)
		}
		return fmt.Errorf("unexpected data folder, left in place: %s", env.Dirs.Data)
	case "stop-engine":
		if r.stopEngine != nil {
			r.stopEngine()
		}
	case "unlock":
		if r.unlock != nil {
			r.unlock()
		}
	case "remove-plist":
		for _, m := range r.methods {
			if t, ok := m.(autostart.Toggler); ok {
				if err := t.SetEnabled(ctx, env.Dirs.InstallExe, false); err != nil {
					return err
				}
			}
		}
	case "bootout-self":
		_, _ = env.Run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), autostart.Label))
	default:
		return fmt.Errorf("unknown step %q", name)
	}
	return nil
}

// winAppFuncs is the real "Installed apps" entry.
type winAppFuncs struct{}

func (winAppFuncs) Register(e winapp.Entry) error { return winapp.Register(e) }
func (winAppFuncs) Unregister() error             { return winapp.Unregister() }

// removedExe is the name a self-removing Windows executable is moved to.
var removedExe = regexp.MustCompile(`^claude-usage-removed-[0-9]+\.exe$`)

// sweepRemoved deletes executables an earlier uninstall moved to %TEMP%
// (only names of exactly that form).
func sweepRemoved(env Env) {
	dir := env.tempDir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if removedExe.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
