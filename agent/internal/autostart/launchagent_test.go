package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wantPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.deep-in-sight.claude-usage</string>
  <key>ProgramArguments</key><array><string>/Users/a b/Library/Application Support/ClaudeUsage/claude-usage</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>/Users/a b/Library/Application Support/ClaudeUsage/agent.log</string>
  <key>StandardErrorPath</key><string>/Users/a b/Library/Application Support/ClaudeUsage/agent.log</string>
</dict>
</plist>
`

func TestPlistText(t *testing.T) {
	dir := "/Users/a b/Library/Application Support/ClaudeUsage"
	eq(t, PlistText(dir+"/claude-usage", dir+"/agent.log"), wantPlist)
	got := PlistText("/Users/R&D <x>/claude-usage", "/l")
	if !strings.Contains(got, "<string>/Users/R&amp;D &lt;x&gt;/claude-usage</string>") {
		t.Fatal(got)
	}
}

func newLaunchAgent(t *testing.T, f *fakeRun) *LaunchAgent {
	home := t.TempDir()
	return &LaunchAgent{Home: home, LogPath: filepath.Join(home, "agent.log"), UID: 501, Run: f.run}
}

func TestLaunchAgentRegisterCalls(t *testing.T) {
	f := &fakeRun{}
	la := newLaunchAgent(t, f)
	exe := "/Users/a b/Library/Application Support/ClaudeUsage/claude-usage"
	must(t, la.Register(bg, exe))
	eq(t, la.Name(), "launchagent")
	eq(t, la.PlistPath(), filepath.Join(la.Home, "Library", "LaunchAgents", "io.github.deep-in-sight.claude-usage.plist"))
	eq(t, f.calls, [][]string{
		{"launchctl", "bootout", "gui/501/io.github.deep-in-sight.claude-usage"},
		{"launchctl", "bootstrap", "gui/501", la.PlistPath()},
	})
	b, err := os.ReadFile(la.PlistPath())
	must(t, err)
	eq(t, string(b), PlistText(exe, la.LogPath))
	st, _ := os.Stat(la.PlistPath())
	eq(t, st.Mode().Perm(), os.FileMode(0o644))

	// bootstrap fails: the plist stays and the caller starts the agent itself
	f = &fakeRun{reply: func(a []string) (string, error) {
		if a[1] == "bootstrap" {
			return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
		}
		return "", nil
	}}
	la = newLaunchAgent(t, f)
	err = la.Register(bg, exe)
	if !errors.Is(err, ErrStartFailed) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(la.PlistPath()); err != nil {
		t.Fatal("plist removed")
	}
}

func TestLaunchAgentState(t *testing.T) {
	f := &fakeRun{}
	la := newLaunchAgent(t, f)
	eq(t, la.State(bg), "not registered")
	must(t, la.SetEnabled(bg, "/x/claude-usage", true))
	f.reply = func([]string) (string, error) {
		return "gui/501/io.github.deep-in-sight.claude-usage = {\n\tactive count = 1\n\tstate = running\n}", nil
	}
	eq(t, la.State(bg), "running")
	f.reply = func([]string) (string, error) { return "Could not find service", errors.New("exit status 113") }
	eq(t, la.State(bg), "not loaded")
}

func TestLaunchAgentToggleStopUnregister(t *testing.T) {
	f := &fakeRun{}
	la := newLaunchAgent(t, f)
	eq(t, la.Enabled(bg), false)
	must(t, la.SetEnabled(bg, "/x/claude-usage", true))
	eq(t, la.Enabled(bg), true)
	eq(t, len(f.calls), 0) // the switch never starts or stops anything
	must(t, la.SetEnabled(bg, "/x/claude-usage", false))
	eq(t, la.Enabled(bg), false)
	eq(t, len(f.calls), 0)

	must(t, la.Register(bg, "/x/claude-usage"))
	f.calls = nil
	must(t, la.Stop(bg))
	eq(t, f.calls, [][]string{{"launchctl", "bootout", "gui/501/io.github.deep-in-sight.claude-usage"}})
	f.calls = nil
	must(t, la.Unregister(bg))
	eq(t, f.calls, [][]string{{"launchctl", "bootout", "gui/501/io.github.deep-in-sight.claude-usage"}})
	if _, err := os.Stat(la.PlistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist left")
	}
	must(t, la.Unregister(bg)) // twice is fine
}
