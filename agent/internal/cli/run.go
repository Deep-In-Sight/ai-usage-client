package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/anthropic"
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
	for _, a := range args {
		if a != "--headless" {
			fmt.Fprintf(env.Stderr, "claude-usage run: unknown argument %q\n", a)
			return 2
		}
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
	defer unlock()

	cfg, err := config.Load(env.Dirs.File(paths.ConfigFile))
	if err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage:", err)
		return 1
	}
	logger := log.New(env.Stderr, "", 0)
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

	ro := readerOptions(cfg, env.Dirs)
	ro.Clock = env.Clock
	reader := creds.NewReader(ro)
	ua := version.UserAgent()
	info := osinfo.Current()
	statusPath := env.Dirs.File(paths.StatusFile)
	statusFailed := false
	e := engine.New(engine.Deps{
		Clock:      env.Clock,
		Logins:     reader,
		Fetcher:    fetcher{client: anthropic.New(anthropic.UsageURL, ua, env.Clock), src: reader},
		Syncer:     protocol.NewClient(cfg.IngestKey, ua, discover.PlatformDialer()),
		Backlog:    bl,
		Config:     cfg,
		StatePath:  env.Dirs.File(paths.StateFile),
		LatestPath: env.Dirs.File(paths.LatestFile),
		Identity: engine.Identity{DeviceID: deviceID, Hostname: info.Hostname, OSUser: info.OSUser, OS: info.OS,
			OSVersion: info.OSVersion, Arch: info.Arch, Version: version.Version, Mode: "headless"},
		Publish: func(s status.Snapshot) {
			if err := status.Write(statusPath, s); err != nil && !statusFailed {
				logger.Printf("status file: %v", err)
				statusFailed = true
			}
		},
		Log: logger,
	})

	ctx, stop := signal.NotifyContext(background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Printf("claude-usage %s started (pid %d, device %s, data %s)", version.Version, os.Getpid(), deviceID, env.Dirs.Data)
	engine.Run(ctx, e, env.Clock)
	logger.Print("claude-usage stopped")
	return 0
}
