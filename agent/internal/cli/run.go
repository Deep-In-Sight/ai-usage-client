package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/logfile"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/osinfo"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/discover"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

// fetcher adapts the usage client to the engine: the reader hands the token
// to the client inside the call and nowhere else.
type fetcher struct {
	client *anthropic.Client
	src    anthropic.TokenSource
}

func (f fetcher) Fetch(ctx context.Context) (anthropic.Result, error) {
	return f.client.Fetch(ctx, f.src)
}

func runCmd(env Env, args []string) int {
	headlessFlag := false
	for _, a := range args {
		if a != "--headless" {
			fmt.Fprintf(env.Stderr, "ai-usage run: unknown argument %q\n", a)
			return 2
		}
		headlessFlag = true
	}
	started := env.Clock.Now()
	// 1: the mode this process starts in.
	headless := headlessFlag || env.goos() == "linux" && osinfo.Headless(env.Getenv)
	clientMode := "tray"
	if headless {
		clientMode = "headless"
	}

	// A newcomer finds out whether it can run before it asks a running agent
	// to stop (review m2): config.json, and that the device id and the
	// backlog can be read.
	cfg, err := config.Load(env.Dirs.File(paths.ConfigFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "ai-usage:", err)
		return 1
	}
	for _, f := range []struct{ name, what string }{{paths.DeviceIDFile, "device id"}, {paths.BacklogFile, "backlog"}} {
		if err := readable(env.Dirs.File(f.name)); err != nil {
			fmt.Fprintf(env.Stderr, "ai-usage: %s: %v\n", f.what, err)
			return 1
		}
	}

	ctx, stop := signal.NotifyContext(env.ctx(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	unlock, replaced, err := lockRun(ctx, env, headless)
	var newer *newerError
	switch {
	case errors.As(err, &newer):
		fmt.Fprintf(env.Stderr, "ai-usage: already running (pid %d, started after this one)\n", newer.holder.PID)
		return 1
	case errors.Is(err, procs.ErrLocked):
		fmt.Fprintln(env.Stderr, "ai-usage: already running")
		return 1
	case err != nil && ctx.Err() != nil:
		return 0 // stopped while waiting for the lock
	case err != nil:
		fmt.Fprintln(env.Stderr, "ai-usage: lock:", err)
		return 1
	}
	var release func() // unlock and close the log, once
	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(unlock) }
	defer func() { release() }()

	info := osinfo.Current()
	scrub := logfile.NewScrubber(logfile.Personal{Home: env.Dirs.Home, User: info.OSUser, Host: info.Hostname}, cfg.IngestKey)
	logOut, closeLog := logOutput(env, scrub)
	logger := log.New(logOut, "", 0)
	// The tray and the systray copy log through the standard logger.
	stdOut, stdFlags := log.Writer(), log.Flags()
	log.SetOutput(logOut)
	log.SetFlags(0)
	release = func() {
		releaseOnce.Do(func() {
			unlock()
			logger.SetOutput(scrub.Writer(env.Stderr))
			log.SetOutput(stdOut)
			log.SetFlags(stdFlags)
			closeLog()
		})
	}
	deviceID, err := config.DeviceID(env.Dirs.File(paths.DeviceIDFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "ai-usage: device id:", err)
		return 1
	}
	bl, err := backlog.Open(env.Dirs.File(paths.BacklogFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "ai-usage: backlog:", err)
		return 1
	}

	ro := readerOptions(cfg, env.Dirs)
	ro.Clock = env.Clock
	reader := creds.NewReader(ro)
	ua := version.UserAgent()
	statusPath := env.Dirs.File(paths.StatusFile)
	statusFailed := false

	initial, err := status.Read(statusPath)
	if err != nil {
		initial = status.Snapshot{}
	}
	var toggle autostart.Toggler
	if m, err := env.Autostart(headless); err == nil {
		toggle, _ = m.(autostart.Toggler)
	}
	ctrl := newController(cfg, initial, nil, toggle)
	ctrl.exe = env.Dirs.InstallExe
	ctrl.iconDir = env.Dirs.File(paths.TrayIconsDir)
	ctrl.offFile = env.Dirs.File(paths.AutostartOffFile)
	updates := make(chan struct{}, 1)

	newSyncer := env.Syncer
	if newSyncer == nil {
		newSyncer = func(cfg config.Config, ua string) engine.Syncer {
			return protocol.NewClient(cfg.IngestKey, ua, discover.PlatformDialer())
		}
	}
	e := engine.New(engine.Deps{
		Clock:      env.Clock,
		Logins:     reader,
		Fetcher:    fetcher{client: anthropic.New(anthropic.UsageURL, ua, env.Clock), src: reader},
		Syncer:     newSyncer(cfg, ua),
		Backlog:    bl,
		Config:     cfg,
		StatePath:  env.Dirs.File(paths.StateFile),
		LatestPath: env.Dirs.File(paths.LatestFile),
		Identity: engine.Identity{DeviceID: deviceID, Hostname: info.Hostname, OSUser: info.OSUser, OS: info.OS,
			OSVersion: info.OSVersion, Arch: info.Arch, Version: version.Version, Mode: clientMode},
		// 2: status file, the tray's snapshot and a wake-up for the tray.
		Publish: func(s status.Snapshot) {
			if err := status.Write(statusPath, s); err != nil && !statusFailed {
				logger.Printf("status file: %v", err)
				statusFailed = true
			}
			ctrl.set(s)
			select {
			case updates <- struct{}{}:
			default:
			}
		},
		Log: logger,
	})
	ctrl.eng = e

	logger.Printf("ai-usage %s started (pid %d, device %s, data %s, %s)", version.Version, os.Getpid(), deviceID, env.Dirs.Data, clientMode)
	if replaced.PID != 0 {
		logger.Printf("took over from the agent that ran before (pid %d)", replaced.PID)
	}

	// 3: the engine runs first, whatever happens with the tray.
	engCtx, cancelEngine := context.WithCancel(ctx)
	engineDone := make(chan struct{})
	go func() {
		engine.Run(engCtx, e, env.Clock)
		close(engineDone)
	}()
	finish := func() int {
		cancelEngine()
		<-engineDone
		logger.Print("ai-usage stopped")
		return 0
	}
	// The tray's Remove: this process stops its engine and lets go of
	// run.lock and the log before removing everything.
	ctrl.remove = func() error {
		r := newRemover(env)
		r.stopEngine = func() { cancelEngine(); <-engineDone }
		r.unlock = release
		err := r.run(removalSteps(env.goos(), true))
		if err != nil {
			fmt.Fprintln(env.Stderr, "ai-usage: remove:", err)
		}
		return err
	}
	marker := env.Dirs.File(paths.NotifyFile)
	withoutTray := func() int {
		_ = os.Remove(marker)
		<-ctx.Done()
		return finish()
	}

	// 4: headless.
	if headless {
		return withoutTray()
	}
	// 5: tray, when there is one.
	available := env.TrayAvailable
	if available == nil {
		available = func(ctx context.Context) (bool, string) { return tray.Available(ctx, env.Getenv) }
	}
	ok, reason := available(ctx)
	if ctx.Err() != nil {
		return finish()
	}
	if !ok {
		e.SetClientMode("headless")
		logger.Printf("no tray (%s): running without an icon", reason)
		_ = os.Remove(marker)
		// The tray host can come later (an extension switched on after
		// login): wait for it to appear on the bus, checking again at
		// least every trayRecheck.
		wait := env.TrayWait
		if wait == nil {
			wait = tray.WaitForHost
		}
		for !ok {
			wait(ctx, trayRecheck)
			if ctx.Err() != nil {
				return finish()
			}
			ok, _ = available(ctx)
			if ctx.Err() != nil {
				return finish()
			}
		}
		e.SetClientMode("tray")
		logger.Print("tray found: showing the icon")
	}
	notice := ""
	if _, err := os.Stat(marker); err == nil {
		_ = os.Remove(marker)
		notice = text.Notice(env.goos())
	}
	show := env.Tray
	if show == nil {
		show = tray.Run
	}
	err = show(ctx, ctrl, updates, notice)
	if errors.Is(err, tray.ErrSessionLost) {
		// The tray cannot reconnect: this run is replaced by a new one,
		// which connects to the session bus again and waits for a tray
		// host. Meanwhile (and while a restart wait keeps restarts from
		// coming in a loop) the engine goes on.
		e.SetClientMode("headless")
		wait, next := restartPlan(restartsFrom(env.Getenv), env.Clock.Now().Sub(started))
		logger.Printf("tray: the connection to the session bus was lost; restarting in %s", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return finish()
		case <-env.Clock.After(wait):
		}
		cancelEngine()
		<-engineDone
		logger.Print("ai-usage restarting")
		release()
		restart := env.Restart
		if restart == nil {
			restart = restartSelf
		}
		if err := restart(append([]string{"run"}, args...), next); err != nil {
			fmt.Fprintln(env.Stderr, "ai-usage: restart:", err)
			return 1
		}
		return 0
	}
	if err != nil {
		e.SetClientMode("headless")
		logger.Printf("tray: %v: running without an icon", err)
		return withoutTray()
	}
	// 6: Quit, Remove or a signal.
	return finish()
}

// readable: the file at p can be read, or there is none yet.
func readable(p string) error {
	_, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// lockRun takes run.lock, writing this process's identity into it. A
// desktop run on Linux or macOS takes it over from an older holder (the
// newest instance wins: at login the new session's run replaces one that
// outlived the old session); a headless run, or any run on Windows, gives
// up when it is held. replaced is the holder that was stopped.
func lockRun(ctx context.Context, env Env, headless bool) (unlock func(), replaced procs.Identity, err error) {
	path := env.Dirs.File(paths.LockFile)
	self, err := procs.Self()
	if err != nil {
		unlock, err := procs.Lock(path)
		return unlock, procs.Identity{}, err
	}
	if headless || env.goos() != "linux" && env.goos() != "darwin" {
		unlock, err := procs.LockAs(path, self)
		return unlock, procs.Identity{}, err
	}
	takeOver := env.TakeOver
	if takeOver == nil {
		takeOver = func(ctx context.Context, path string, self procs.Identity) (func(), procs.Identity, error) {
			return procs.TakeOver(ctx, path, self, procs.TakeOverOptions{})
		}
	}
	unlock, replaced, err = takeOver(ctx, path, self)
	if errors.Is(err, procs.ErrNewer) {
		return nil, procs.Identity{}, &newerError{replaced}
	}
	return unlock, replaced, err
}

// newerError: run.lock is held by a run that started after this one.
type newerError struct{ holder procs.Identity }

func (e *newerError) Error() string { return "held by a newer run" }

// Restarts after a lost session bus: each run lives at least restartBase
// doubled for every quick restart before it (at most restartMax), so that a
// bus that keeps failing cannot make restarts spin. A run that lived
// restartReset starts the count again.
var (
	restartBase  = 5 * time.Second
	restartMax   = 10 * time.Minute
	restartReset = 10 * time.Minute
)

// restartPlan: how long a run that lived for lived waits before it
// restarts, and the count the new run gets, after prev quick restarts.
func restartPlan(prev int, lived time.Duration) (wait time.Duration, next int) {
	if lived >= restartReset || prev < 0 {
		prev = 0
	}
	life := restartMax
	if prev < 20 {
		life = min(restartBase<<prev, restartMax)
	}
	return max(0, life-lived), prev + 1
}

// restartsFrom reads the count a restarted run was given.
func restartsFrom(getenv func(string) string) int {
	n, err := strconv.Atoi(getenv(restartsEnv))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// restartEnviron is the environment of the restarted run: environ with
// restartsEnv set to n, and the session bus address moved to the runtime
// folder's bus when the old one no longer answers and that one does (a
// desktop that gives each session its own bus address; review m4).
func restartEnviron(environ []string, n int, reachable func(addr string) bool) []string {
	get := func(k string) string {
		for _, kv := range environ {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				return v
			}
		}
		return ""
	}
	bus, moved := tray.SessionBusFallback(get, reachable)
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		switch {
		case strings.HasPrefix(kv, restartsEnv+"="):
		case moved && strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS="):
			out = append(out, "DBUS_SESSION_BUS_ADDRESS="+bus)
		default:
			out = append(out, kv)
		}
	}
	return append(out, restartsEnv+"="+strconv.Itoa(n))
}

// restartsEnv carries the count of quick restarts to the new run.
const restartsEnv = "AI_USAGE_RESTARTS"

// trayRecheck is how often a run without a tray looks for one again even
// when no tray host appeared on the bus.
var trayRecheck = 5 * time.Minute

// logOutput is where run logs, every line scrubbed: the data folder's
// agent.log (each line stamped, moved to agent.log.1 past 1 MiB, Go crash
// reports included, scrubbed at the next start) and standard error. The agent that install starts has
// no standard error anyone reads (Linux, macOS: /dev/null; Windows: a GUI
// process has none), so the file is the log users can read. When standard
// error already is agent.log (launchd on macOS), only standard error is
// written, and the file is cut at start past 1 MiB.
func logOutput(env Env, scrub *logfile.Scrubber) (io.Writer, func()) {
	p := env.Dirs.File(paths.LogFile)
	// Crash reports from the run before reached the files unscrubbed.
	for _, f := range []string{p, p + ".1"} {
		_ = logfile.ScrubFile(f, scrub)
	}
	if sameFile(env.Stderr, p) {
		if st, err := os.Stat(p); err == nil && st.Size() > logfile.Max {
			_ = os.Truncate(p, 0)
		}
		return scrub.Writer(env.Stderr), func() {}
	}
	f, err := logfile.Open(p, logfile.Max)
	if err != nil {
		return scrub.Writer(env.Stderr), func() {}
	}
	f.SendCrashes()
	// The file first: io.MultiWriter stops at the first failing writer, and
	// a GUI process's standard error is not valid.
	return scrub.Writer(io.MultiWriter(f, env.Stderr)), func() { f.Close() }
}

// sameFile: w is an open file that is the file at p.
func sameFile(w io.Writer, p string) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(p)
	return err == nil && os.SameFile(a, b)
}
