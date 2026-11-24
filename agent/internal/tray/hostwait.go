package tray

import (
	"context"
	"strings"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

const (
	watcherName = "org.kde.StatusNotifierWatcher"
	// hostRetry: how soon a lost or missing session bus is tried again.
	hostRetry = 2 * time.Second
	// hostPoll: besides the NameOwnerChanged signal, the watcher's name is
	// checked this often, in case a signal was missed.
	hostPoll = 30 * time.Second
	// minWatcherWait is the shortest AI_USAGE_TRAY_WAIT taken.
	minWatcherWait = 100 * time.Millisecond
)

// WatcherWaitFrom is how long a run waits for the tray host at start
// before it goes on without an icon: WatcherWait, or AI_USAGE_TRAY_WAIT (a
// Go duration such as "3s", for tests; at least 100 ms).
func WatcherWaitFrom(getenv func(string) string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(getenv("AI_USAGE_TRAY_WAIT")))
	if err != nil || d <= 0 {
		return WatcherWait
	}
	return max(d, minWatcherWait)
}

// hostConn is a session bus connection as waitForHost uses it.
type hostConn interface {
	// Has asks the bus whether the watcher's name has an owner.
	Has(ctx context.Context) (bool, error)
	// Appeared receives when the watcher's name gets a new owner.
	Appeared() <-chan struct{}
	// Closed is closed when the connection ends.
	Closed() <-chan struct{}
	Close()
}

// waitForHost waits until a StatusNotifierWatcher is on the session bus:
// at once when its name appears (NameOwnerChanged), else at the next
// check every hostPoll. A missing or lost bus is connected to again every
// hostRetry. It returns false when ctx ends or after limit (0: no limit).
func waitForHost(ctx context.Context, connect func() (hostConn, error), clk clock.Clock, limit time.Duration) bool {
	var deadline <-chan time.Time
	if limit > 0 {
		deadline = clk.After(limit)
	}
	for {
		if c, err := connect(); err == nil {
			found, lost := watchHost(ctx, c, clk, deadline)
			c.Close()
			if found {
				return true
			}
			if !lost {
				return false
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-clk.After(hostRetry):
		}
	}
}

// watchHost watches one connection: found, or lost (connect again), or
// neither (ctx or the deadline ended the wait).
func watchHost(ctx context.Context, c hostConn, clk clock.Clock, deadline <-chan time.Time) (found, lost bool) {
	for {
		has, err := c.Has(ctx)
		if ctx.Err() != nil {
			return false, false
		}
		if err != nil {
			return false, true
		}
		if has {
			return true, false
		}
		select {
		case <-c.Appeared():
		case <-c.Closed():
			return false, true
		case <-clk.After(hostPoll):
		case <-ctx.Done():
			return false, false
		case <-deadline:
			return false, false
		}
	}
}
