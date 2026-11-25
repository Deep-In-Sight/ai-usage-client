//go:build !linux

package trayhelper

import (
	"bufio"
	"context"
	"io"
	"log"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// runBridge: no GNOME bridge outside Linux (the agent never asks for one).
func runBridge(context.Context, *bufio.Reader, io.Writer, bool) int {
	log.Print("tray-helper: no GNOME Shell on this system")
	return trayproto.ExitUnsupported
}
