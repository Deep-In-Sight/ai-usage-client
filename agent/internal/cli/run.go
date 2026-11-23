package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/anthropic"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/creds"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/osinfo"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/discover"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/text"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/tray"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/version"
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
			fmt.Fprintf(env.Stderr, "claude-usage run: unknown argument %q\n", a)
			return 2
		}
		headlessFlag = true
	}
	unlock, err := procs.Lock(env.Dirs.File(paths.LockFile))
	if errors.Is(err, procs.ErrLocked) {
		fmt.Fprintln(env.Stderr, "claude-usage: already running")
		return 1
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage: lock:", err)
		return 1
	}
	var release func() // unlock and close the log, once
	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(unlock) }
	defer func() { release() }()

	cfg, err := config.Load(env.Dirs.File(paths.ConfigFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage:", err)
		return 1
	}
	logOut, closeLog := logOutput(env)
	logger := log.New(logOut, "", 0)
	release = func() {
		releaseOnce.Do(func() {
			unlock()
			logger.SetOutput(env.Stderr)
			closeLog()
		})
	}
	if env.goos() == "windows" {
		logger.SetFlags(log.LstdFlags)
	}
	deviceID, err := config.DeviceID(env.Dirs.File(paths.DeviceIDFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage: device id:", err)
		return 1
	}
	bl, err := backlog.Open(env.Dirs.File(paths.BacklogFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage: backlog:", err)
		return 1
	}

	// 1: the mode this process starts in.
	headless := headlessFlag || env.goos() == "linux" && osinfo.Headless(env.Getenv)
	clientMode := "tray"
	if headless {
		clientMode = "headless"
	}

	ro := readerOptions(cfg, env.Dirs)
	ro.Clock = env.Clock
	reader := creds.NewReader(ro)
	ua := version.UserAgent()
	info := osinfo.Current()
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

	ctx, stop := signal.NotifyContext(env.ctx(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Printf("claude-usage %s started (pid %d, device %s, data %s, %s)", version.Version, os.Getpid(), deviceID, env.Dirs.Data, clientMode)

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
		logger.Print("claude-usage stopped")
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
			fmt.Fprintln(env.Stderr, "claude-usage: remove:", err)
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
		return withoutTray()
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
	if err := show(ctx, ctrl, updates, notice); err != nil {
		e.SetClientMode("headless")
		logger.Printf("tray: %v: running without an icon", err)
		return withoutTray()
	}
	// 6: Quit, Remove or a signal.
	return finish()
}

// logOutput is where run logs: standard error, and on Windows (where the
// agent has no console) also the data folder's agent.log, restarted when it
// grows past 1 MiB. On macOS launchd writes agent.log; it is cut the same way.
func logOutput(env Env) (io.Writer, func()) {
	if env.goos() == "darwin" {
		// launchd appends standard output and error to agent.log for ever.
		p := env.Dirs.File(paths.LogFile)
		if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
			_ = os.Truncate(p, 0)
		}
	}
	if env.goos() != "windows" {
		return env.Stderr, func() {}
	}
	p := env.Dirs.File(paths.LogFile)
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		_ = os.Remove(p)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return env.Stderr, func() {}
	}
	// The file first: io.MultiWriter stops at the first failing writer, and
	// a GUI process's standard error is not valid.
	return io.MultiWriter(f, env.Stderr), func() { f.Close() }
}
