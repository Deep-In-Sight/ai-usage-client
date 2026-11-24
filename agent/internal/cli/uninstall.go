package cli

import (
	"fmt"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
)

func uninstallCmd(env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(env.Stderr, "claude-usage uninstall: unknown argument %q\n", args[0])
		return 2
	}
	if err := newRemover(env).run(removalSteps(env.goos(), false)); err != nil {
		fmt.Fprintln(env.Stderr, "claude-usage uninstall:", err)
		if !env.hasConsole() {
			env.alert(text.App, err.Error())
		}
		return 1
	}
	fmt.Fprintln(env.Stdout, text.Removed)
	if !env.hasConsole() {
		env.alert(text.App, text.AlertRemoved)
	}
	return 0
}
