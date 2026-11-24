package supervisor

import (
	"slices"
	"testing"
)

// A helper starts with a small allow-list of the agent's environment: what
// a tray icon needs to reach the desktop session, and the OS's basics
// (stage-2 review m-4). Sign-in folders, keys and proxies stay behind.
func TestHelperEnvironmentAllowList(t *testing.T) {
	common := []string{"PATH=/bin", "HOME=/h", "CLAUDE_CONFIG_DIR=/secret", "ANTHROPIC_API_KEY=sk-x", "OPENAI_API_KEY=x",
		"HTTPS_PROXY=http://p", "SSH_AUTH_SOCK=/s", "AI_USAGE_TEST_FAKE_PROVIDERS=claude", "AI_USAGE_TRAY_WAIT=3s",
		"GITHUB_TOKEN=x", "CODEX_HOME=/c", "LANG=en_US.UTF-8", "LC_TIME=de_DE.UTF-8"}
	for _, c := range []struct {
		goos       string
		extra      []string
		keep, drop []string
	}{
		{"linux",
			[]string{"DISPLAY=:0", "WAYLAND_DISPLAY=wayland-0", "XAUTHORITY=/x", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1/bus",
				"XDG_RUNTIME_DIR=/run/user/1", "XDG_SESSION_TYPE=wayland", "XDG_CURRENT_DESKTOP=ubuntu:GNOME", "XDG_DATA_DIRS=/usr/share",
				"DESKTOP_SESSION=ubuntu", "USER=u", "LOGNAME=u", "TZ=UTC"},
			[]string{"PATH", "HOME", "LANG", "LC_TIME", "AI_USAGE_TRAY_WAIT", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
				"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "XDG_DATA_DIRS",
				"DESKTOP_SESSION", "USER", "LOGNAME", "TZ"},
			[]string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "HTTPS_PROXY", "SSH_AUTH_SOCK",
				"AI_USAGE_TEST_FAKE_PROVIDERS", "GITHUB_TOKEN", "CODEX_HOME"}},
		{"darwin",
			[]string{"TMPDIR=/var/folders/x", "USER=u", "LOGNAME=u", "SHELL=/bin/zsh", "__CF_USER_TEXT_ENCODING=0x1F5:0x0:0x0",
				"XPC_SERVICE_NAME=io.github.deep-in-sight.ai-usage", "XPC_FLAGS=0x0", "DISPLAY=:0"},
			[]string{"PATH", "HOME", "LANG", "LC_TIME", "AI_USAGE_TRAY_WAIT", "TMPDIR", "USER", "LOGNAME", "SHELL",
				"__CF_USER_TEXT_ENCODING", "XPC_SERVICE_NAME", "XPC_FLAGS"},
			[]string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "SSH_AUTH_SOCK", "GITHUB_TOKEN", "DISPLAY", "AI_USAGE_TEST_FAKE_PROVIDERS"}},
		{"windows",
			[]string{"SystemRoot=C:\\Windows", "SYSTEMDRIVE=C:", "windir=C:\\Windows", "ComSpec=C:\\Windows\\system32\\cmd.exe",
				"Path=C:\\Windows", "PATHEXT=.COM;.EXE", "TEMP=C:\\T", "TMP=C:\\T", "USERPROFILE=C:\\Users\\u", "APPDATA=C:\\A",
				"LOCALAPPDATA=C:\\L", "USERNAME=u", "PROCESSOR_ARCHITECTURE=AMD64", "NUMBER_OF_PROCESSORS=4", "OS=Windows_NT",
				"ProgramFiles(x86)=C:\\P86", "=C:=C:\\work", "SESSIONNAME=Console", "OneDrive=C:\\Users\\u\\OneDrive"},
			[]string{"SystemRoot", "SYSTEMDRIVE", "windir", "ComSpec", "Path", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "APPDATA",
				"LOCALAPPDATA", "USERNAME", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS", "OS", "ProgramFiles(x86)", "=C:",
				"SESSIONNAME", "AI_USAGE_TRAY_WAIT"},
			[]string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "HTTPS_PROXY", "GITHUB_TOKEN", "OneDrive", "LANG", "HOME"}},
	} {
		got := helperEnviron(c.goos, append(slices.Clone(common), c.extra...), []string{"MY_TEST_SWITCH"})
		names := map[string]bool{}
		for _, kv := range got {
			k, _ := envName(kv)
			names[k] = true
		}
		for _, k := range c.keep {
			if !names[k] {
				t.Errorf("%s: %s dropped", c.goos, k)
			}
		}
		for _, k := range c.drop {
			if names[k] {
				t.Errorf("%s: %s passed on", c.goos, k)
			}
		}
		// a test's own switch is passed when asked for
		if got := helperEnviron(c.goos, []string{"MY_TEST_SWITCH=1"}, []string{"MY_TEST_SWITCH"}); len(got) != 1 {
			t.Errorf("%s: test switch %v", c.goos, got)
		}
	}
	// Windows names are compared without case
	if got := helperEnviron("windows", []string{"SYSTEMROOT=C:\\Windows", "path=C:\\"}, nil); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}
