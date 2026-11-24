package cli

import (
	"fmt"
	"log"
	"os"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayhelper"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// trayHelperCmd is `ai-usage tray-helper`: one tray icon, drawn from the
// lines the running agent writes to its standard input (design 6.4); what
// it is for (provider, icon id, icon folder) comes in the first line, so
// the command line, which other users can read, names nothing. It is
// started by the agent only and is not in the usage text.
func trayHelperCmd(env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(env.Stderr, "ai-usage tray-helper: unknown argument %q\n", args[0])
		return 2
	}
	token := env.Getenv(trayproto.EnvToken)
	// The helper's messages go to standard error, which the agent copies
	// into agent.log (stamped and scrubbed there).
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(env.Stderr)
	log.SetFlags(0)
	defer func() { log.SetOutput(out); log.SetFlags(flags) }()
	in := env.Stdin
	if in == nil {
		in = os.Stdin
	}
	return trayhelper.Run(env.ctx(), in, env.Stdout, token)
}
