//go:build !linux

package shellbridge

import (
	"bufio"
	"context"
	"io"
	"log"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// Run ends at once: there is no GNOME Shell on this system.
func Run(ctx context.Context, in *bufio.Reader, out io.Writer, o Options) int {
	log.Print("the GNOME bridge runs on Linux only")
	return trayproto.ExitUnsupported
}
