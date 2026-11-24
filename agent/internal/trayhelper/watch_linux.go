package trayhelper

import (
	"context"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// watchSession waits until the session bus has a tray host coming (back)
// (waitForChange on the real bus).
var watchSession = func(ctx context.Context) bool { return waitForChange(ctx, dialHost, clock.Real{}) }
