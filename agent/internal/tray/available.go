package tray

import (
	"context"
	"errors"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/osinfo"
)

// WatcherWait is how long Linux waits for a StatusNotifierWatcher (the
// desktop's tray host) to appear after login.
const WatcherWait = 60 * time.Second

const watcherPoll = 2 * time.Second

var errNoBus = errors.New("tray: no session bus")

// availableWith is the Linux rule with its inputs replaced: no desktop
// session, no session bus (hasOwner fails) or no watcher within WatcherWait
// means no tray.
func availableWith(ctx context.Context, getenv func(string) string, hasOwner func() (bool, error), clk clock.Clock) (bool, string) {
	if osinfo.Headless(getenv) {
		return false, "no desktop session"
	}
	deadline := clk.Now().Add(WatcherWait)
	for {
		ok, err := hasOwner()
		if err != nil {
			return false, "no session bus"
		}
		if ok {
			return true, ""
		}
		if !clk.Now().Before(deadline) {
			return false, "no StatusNotifierWatcher"
		}
		select {
		case <-ctx.Done():
			return false, "stopped"
		case <-clk.After(watcherPoll):
		}
	}
}
