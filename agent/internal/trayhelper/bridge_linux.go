package trayhelper

import (
	"bufio"
	"context"
	"io"
	"os"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/shellbridge"
)

// runBridge is the helper in the shell role: the GNOME bridge.
func runBridge(ctx context.Context, in *bufio.Reader, out io.Writer, enableOnce bool) int {
	home, _ := os.UserHomeDir()
	return shellbridge.Run(ctx, in, out, shellbridge.Options{EnableOnce: enableOnce, Getenv: os.Getenv, Home: home, Clock: clock.Real{}})
}
