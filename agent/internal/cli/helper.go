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
	helperSwitches(env)
	in := env.Stdin
	if in == nil {
		in = os.Stdin
	}
	return trayhelper.Run(env.ctx(), in, env.Stdout, token)
}

// testActiveMenuEnv: the tray helper of a CI build (version 0.0.0-ci)
// makes itself the active app before its menu shows (macOS), so that CI
// judges Return in an active app's menu (tools/macmenu). Any other build
// ignores it.
const testActiveMenuEnv = "AI_USAGE_TEST_ACTIVE_MENU"

// activateOnMenuOpen is trayhelper.ActivateOnMenuOpen (tests replace it).
var activateOnMenuOpen = trayhelper.ActivateOnMenuOpen

// helperSwitches applies the test switches a CI build's tray helper reads
// (testSwitches).
func helperSwitches(env Env) {
	if isCIBuild() && env.Getenv(testActiveMenuEnv) != "" {
		log.Printf("CI build: test switch %s on", testActiveMenuEnv)
		activateOnMenuOpen()
	}
}
