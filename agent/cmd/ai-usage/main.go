// Command ai-usage is the AI Usage agent.
package main

import (
	"os"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/cli"
)

func main() { os.Exit(cli.Main(cli.DefaultEnv())) }
