// Command claude-usage is the Claude Usage agent.
package main

import (
	"os"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/cli"
)

func main() { os.Exit(cli.Main(cli.DefaultEnv())) }
