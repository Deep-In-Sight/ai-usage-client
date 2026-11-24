package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

const actAsHelperEnv = "CLI_TEST_ACT_AS_HELPER"

// TestMain lets this test binary stand in for the tray helper the agent
// starts (its own executable with "tray-helper").
func TestMain(m *testing.M) {
	if os.Getenv(actAsHelperEnv) == "1" && len(os.Args) > 1 && os.Args[1] == "tray-helper" {
		os.Exit(actAsHelper())
	}
	code := m.Run()
	if builtFakeCodex.dir != "" {
		os.RemoveAll(builtFakeCodex.dir)
	}
	os.Exit(code)
}

// actAsHelper speaks the helper protocol: it logs a line with a secret and
// a home path on standard error, says ready, clicks Quit and ends on quit.
func actAsHelper() int {
	r := bufio.NewReader(os.Stdin)
	first, err := trayproto.Decode(r)
	if err != nil || first.Token == "" || first.Token != os.Getenv(trayproto.EnvToken) {
		return trayproto.ExitRefused
	}
	fmt.Fprintf(os.Stderr, "systray error: key test-ingest-key-0123456789 under %s\n", os.Getenv("CLI_TEST_HOME"))
	for {
		m, err := trayproto.Decode(r)
		if err != nil || m.Type == trayproto.TypeQuit {
			return trayproto.ExitOK
		}
		if m.Type == trayproto.TypeView {
			_ = trayproto.Encode(os.Stdout, trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeReady})
			_ = trayproto.Encode(os.Stdout, trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeClick, ID: "quit"})
		}
	}
}

// The agent starts its own executable as the helper; what the helper
// writes to standard error reaches agent.log, marked and scrubbed.
func TestHelperOutputReachesAgentLogScrubbed(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	te.GOOS = "linux"
	te.vars["XDG_SESSION_TYPE"] = "wayland"
	te.Helper = nil
	te.Exe = os.Args[0]
	te.HelperEnv = []string{actAsHelperEnv, "CLI_TEST_HOME"}
	t.Setenv(actAsHelperEnv, "1")
	t.Setenv("CLI_TEST_HOME", te.home)
	eq(t, te.main("run"), 0)
	got := read(t, te.Dirs.File(paths.LogFile))
	contains(t, got, "[tray none] systray error: key [ingest key] under ~\n")
	for _, bad := range []string{"test-ingest-key-0123456789", te.home} {
		if strings.Contains(got, bad) {
			t.Fatalf("%q in agent.log:\n%s", bad, got)
		}
	}
}

// "tray-helper" is for the agent only: without the agent's first line it
// ends at once, showing nothing.
func TestTrayHelperCommandNeedsTheAgent(t *testing.T) {
	te := newEnv(t)
	te.Stdin = strings.NewReader(`{"v":1,"type":"hello","token":"guess"}` + "\n")
	te.vars[trayproto.EnvToken] = "the-real-one"
	eq(t, te.main("tray-helper"), trayproto.ExitRefused)
	contains(t, te.err.String(), "started by the agent itself")
	te.Stdin = strings.NewReader("")
	eq(t, te.main("tray-helper"), trayproto.ExitRefused)
	// it takes no arguments: what it is for comes in the agent's first line
	for _, bad := range [][]string{{"--uid", "x"}, {"--provider"}, {"--provider", "claude"}, {"--colour", "red"}} {
		eq(t, te.main(append([]string{"tray-helper"}, bad...)...), 2)
	}
	// not in the usage text
	te.main("help")
	if strings.Contains(te.out.String(), "tray-helper") {
		t.Fatal("tray-helper in the usage text")
	}
}
