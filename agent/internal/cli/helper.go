package cli

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayhelper"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// trayHelperCmd is `ai-usage tray-helper --provider <p> --uid <n>
// --icon-dir <dir>`: one tray icon, drawn from the lines the running agent
// writes to its standard input (design 6.4). It is started by the agent
// only and is not in the usage text.
func trayHelperCmd(env Env, args []string) int {
	var o trayhelper.Options
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			fmt.Fprintf(env.Stderr, "ai-usage tray-helper: %s needs a value\n", args[i])
			return 2
		}
		v := args[i+1]
		switch args[i] {
		case "--provider":
			if v != "none" {
				o.Provider = v
			}
		case "--uid":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				fmt.Fprintln(env.Stderr, "ai-usage tray-helper: --uid:", err)
				return 2
			}
			o.UID = uint32(n)
		case "--icon-dir":
			o.IconDir = v
		default:
			fmt.Fprintf(env.Stderr, "ai-usage tray-helper: unknown argument %q\n", args[i])
			return 2
		}
		i++
	}
	o.Token = env.Getenv(trayproto.EnvToken)
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
	return trayhelper.Run(env.ctx(), in, env.Stdout, o)
}
