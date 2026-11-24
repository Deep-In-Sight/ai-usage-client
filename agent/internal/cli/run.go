package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/logfile"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/osinfo"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider/claude"
	codexprov "github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider/codex"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayhelper"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

func runCmd(env Env, args []string) int {
	headlessFlag := false
	for _, a := range args {
		if a != "--headless" {
			fmt.Fprintf(env.Stderr, "ai-usage run: unknown argument %q\n", a)
			return 2
		}
		headlessFlag = true
	}
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
	// The engine, the tray helpers' copied output and the standard logger
	// write from several goroutines: one line at a time.
	logOut = &lockedWriter{w: logOut}
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
	machineKey, machineSource := env.machine() // read once; never logged
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
	ctrl.offFile = env.Dirs.File(paths.AutostartOffFile)
	updates := make(chan struct{}, 1)

	codexHome := codex.Home(cfg, env.Dirs.Home)
	codexUsageURL, _ := codex.UsageURL("") // the address is set from config.toml at each fetch
	claudeTransport, codexTransport := vendorTransports(env)
	if _, off := codexTransport.(offlineTransport); off {
		logger.Print("CI build: no request to Anthropic or OpenAI")
	}
	e := engine.New(engine.Deps{
		Clock: env.Clock,
		// Claude: the reader hands the token to the usage client inside the
		// call and nowhere else.
		// Codex: the same, with the Codex CLI's sign-in (read-only) and
		// chatgpt.com; its lane is there on every PC (signed out: account
		// null), as the server expects (design 6.8).
		Providers: []provider.Provider{
			claude.New(reader, anthropic.NewWith(anthropic.UsageURL, ua, env.Clock, claudeTransport), env.Clock),
			codexprov.New(codex.NewReader(codexHome, env.Clock),
				codex.NewClientWith(codexUsageURL, ua, env.Clock, codexTransport), codexFallback(env, codexHome)),
		},
		Syncer:           env.syncer(cfg, ua),
		Backlog:          bl,
		Config:           cfg,
		StatePath:        env.Dirs.File(paths.StateFile),
		LatestPath:       func(id provider.ID) string { return env.Dirs.File(paths.LatestFileFor(id)) },
		LegacyLatestPath: env.Dirs.File(paths.LatestFile),
		Identity: engine.Identity{DeviceID: deviceID, Hostname: info.Hostname, OSUser: info.OSUser, OS: info.OS,
			// until a tray helper shows an icon, the agent has none
			OSVersion: info.OSVersion, Arch: info.Arch, Version: version.Version, Mode: "headless",
			MachineKey: machineKey, MachineSource: machineSource},
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
	// 5: tray helpers (design 6.4). The engine reports no icon until a
	// helper shows one.
	if !trayhelper.Compiled {
		e.SetClientMode("headless")
		logger.Print("no tray (tray not compiled into this build): running without an icon")
		return withoutTray()
	}
	e.SetClientMode("headless")
	notice := ""
	if _, err := os.Stat(marker); err == nil {
		_ = os.Remove(marker)
		notice = text.Notice(env.goos())
	}
	sup := supervisor.New(supervisor.Options{Spawn: env.helperSpawner(logger), IconDir: env.iconDir,
		Standby: watchesDesktop(env.goos()), Clock: env.Clock, Log: logger})
	loop := &trayLoop{env: env, ctrl: ctrl, sup: sup, updates: updates, log: logger, setMode: e.SetClientMode,
		known: []provider.ID{provider.Claude, provider.Codex}, extra: testHelpers(env)}
	// The tray's Remove: the helpers end, this process stops its engine
	// and lets go of run.lock and the log before removing everything.
	loop.remove = func() error {
		r := newRemover(env)
		r.stopHelpers = sup.Stop
		r.stopEngine = func() { cancelEngine(); <-engineDone }
		r.unlock = release
		err := r.run(removalSteps(env.goos(), true))
		if err != nil {
			fmt.Fprintln(env.Stderr, "ai-usage: remove:", err)
		}
		return err
	}
	loop.run(ctx, notice)
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

// lockedWriter writes one Write at a time.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
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

// testCodexCLIEnv names a fake Codex CLI (an absolute path) that a CI
// build (version 0.0.0-ci) runs as the Codex lane's fallback (codex
// app-server), the only executable it may run: nothing is looked for, so
// no real codex on a runner can be reached. Any other build ignores it.
const testCodexCLIEnv = "AI_USAGE_TEST_CODEX_FALLBACK"

// codexFallback is the Codex lane's fallback, nil in every build but a CI
// build with testCodexCLIEnv: a released agent never starts the Codex CLI
// in any form (not app-server, not --version), because a started codex
// may renew and rewrite the user's sign-in by itself (review I-1, ruling
// F1). Codex usage then comes only from the direct request, and when that
// fails, its failure is what is reported. The process-wide switch in
// internal/codex (AllowCLIRuns) is set to match, so nothing else in the
// process can start codex either.
func codexFallback(env Env, codexHome string) *codex.AppServer {
	cli := env.Getenv(testCodexCLIEnv)
	on := buildVersion() == "0.0.0-ci" && filepath.IsAbs(cli)
	codex.AllowCLIRuns(on)
	if !on {
		return nil
	}
	return &codex.AppServer{
		CLIs:    &codex.CLIFinder{List: func() []string { return []string{cli} }, Version: env.CodexVersion},
		Home:    codexHome,
		Version: version.Version,
	}
}

// vendorTransports are the transports of the usage requests to Anthropic
// and OpenAI. A CI build (version 0.0.0-ci) refuses every such request
// unless a test gave its own: CI runners have fake sign-ins only, and a
// lane the server does not serve, or every lane while the server cannot
// be reached, fetches on its own, so not even a fake token may leave a
// runner for those hosts (review N-5, ruling F5).
func vendorTransports(env Env) (claude, codex http.RoundTripper) {
	claude, codex = env.ClaudeTransport, env.CodexTransport
	if buildVersion() == "0.0.0-ci" {
		if claude == nil {
			claude = offlineTransport{"Anthropic"}
		}
		if codex == nil {
			codex = offlineTransport{"OpenAI"}
		}
	}
	return claude, codex
}

// offlineTransport refuses every request, sending nothing.
type offlineTransport struct{ host string }

func (o offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("CI build: no request to " + o.host)
}
