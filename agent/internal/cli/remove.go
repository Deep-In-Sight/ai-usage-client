package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/osinfo"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/discover"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/winapp"
)

// removalSteps is the order in which everything is removed: by "uninstall"
// (another process) or by the tray's Remove (inside the running agent,
// which must first end its tray helpers, stop its engine and let go of
// run.lock; design 6.4).
// notify-server, the uninstall sync, comes once no agent of this user runs
// any more (a running one would undo the server's retirement with its next
// sync, design D8 rules 4-5) and while config.json is still there.
func removalSteps(goos string, inProcess bool) []string {
	switch {
	case goos == "windows" && !inProcess:
		return []string{"stop", "kill", "notify-server", "unregister-autostart", "unregister-app", "remove-exe", "remove-install-dir", "remove-data"}
	case goos == "windows":
		return []string{"stop-helpers", "stop-engine", "notify-server", "unlock", "unregister-autostart", "unregister-app", "remove-exe", "remove-install-dir", "remove-data"}
	case goos == "darwin" && !inProcess:
		return []string{"stop", "kill", "notify-server", "unregister-autostart", "remove-data"}
	case goos == "darwin":
		// bootout ends this process, so it comes last.
		return []string{"stop-helpers", "stop-engine", "notify-server", "unlock", "remove-plist", "remove-data", "bootout-self"}
	case !inProcess:
		return []string{"stop", "kill", "notify-server", "unregister-autostart", "remove-exe", "remove-data"}
	}
	return []string{"stop-helpers", "stop-engine", "notify-server", "unlock", "unregister-autostart", "remove-exe", "remove-data"}
}

// notifyTimeout bounds the uninstall sync: removal waits for it this long
// at most.
const notifyTimeout = 3 * time.Second

// notifyServer sends the uninstall sync (design D8 rule 4): the server
// retires this device at once, so a reinstall is the same PC. Without a
// config or a device id there is nothing to tell; a failure is reported
// and removal goes on; a server without schema 2 has nothing to retire.
func notifyServer(env Env) {
	cfg, err := config.Load(env.Dirs.File(paths.ConfigFile))
	if err != nil {
		return
	}
	b, err := os.ReadFile(env.Dirs.File(paths.DeviceIDFile))
	id := strings.TrimSpace(string(b))
	if err != nil || id == "" {
		return
	}
	st := config.LoadState(env.Dirs.File(paths.StateFile))
	info := osinfo.Current()
	key, source := env.machine()
	req := engine.UninstallRequest(engine.Identity{DeviceID: id, Hostname: info.Hostname, OSUser: info.OSUser, OS: info.OS,
		OSVersion: info.OSVersion, Arch: info.Arch, Version: version.Version, Mode: "headless", MachineKey: key, MachineSource: source})
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	syncer := env.syncer(cfg, version.UserAgent())
	var last error
	for _, base := range discover.Candidates(st.LastGoodURL, st.ServerURLs, cfg.ServerURLs) {
		if ctx.Err() != nil {
			break
		}
		_, err := syncer.SyncV2(ctx, base, req)
		if err == nil || errors.Is(err, protocol.ErrUnsupportedSchema) {
			return
		}
		last = err
	}
	if ctx.Err() != nil {
		last = errors.New("no answer within 3 s")
	}
	if last != nil {
		fmt.Fprintln(env.Stderr, "ai-usage uninstall: could not tell the server (removing anyway):", last)
	}
}

// remover carries out removal steps. Linger is never touched.
type remover struct {
	env         Env
	methods     []autostart.Method
	stopHelpers func()
	stopEngine  func()
	unlock      func()
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
				fmt.Fprintln(env.Stderr, "ai-usage uninstall:", err)
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
			dst := filepath.Join(env.tempDir(), fmt.Sprintf("ai-usage-removed-%d.exe", os.Getpid()))
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
		case "ai-usage", "AIUsage":
			return os.RemoveAll(env.Dirs.Data)
		}
		return fmt.Errorf("unexpected data folder, left in place: %s", env.Dirs.Data)
	case "stop-helpers":
		if r.stopHelpers != nil {
			r.stopHelpers()
		}
	case "stop-engine":
		if r.stopEngine != nil {
			r.stopEngine()
		}
	case "notify-server":
		notifyServer(env)
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
var removedExe = regexp.MustCompile(`^ai-usage-removed-[0-9]+\.exe$`)

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
