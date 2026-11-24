package supervisor

import (
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// A helper starts with a small part of the agent's environment (stage-2
// review m-4): what a tray icon needs to reach the desktop session and
// what the OS's own libraries expect. Everything else stays with the
// agent: the sign-in folder (CLAUDE_CONFIG_DIR), API keys, proxies, SSH
// agents, CI tokens. Where it is unsure whether the OS's tray or its
// libraries need a variable (Windows, macOS), it is kept.
var (
	helperEnvLinux = []string{
		// the desktop session: X11/Wayland display, session bus, runtime
		// folder (the bus's usual place); XDG_* below
		"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS",
		"DESKTOP_SESSION", "GDMSESSION", "KDE_FULL_SESSION", "KDE_SESSION_VERSION",
		// the user and the basics
		"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "TZ", "LANG", "LANGUAGE",
	}
	helperEnvDarwin = []string{
		"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "TZ", "LANG",
		// CoreFoundation's text encoding and launchd's job
		"__CF_USER_TEXT_ENCODING", "XPC_SERVICE_NAME", "XPC_FLAGS", "__CFBundleIdentifier",
	}
	helperEnvWindows = []string{
		// the system's own folders and settings, which system DLLs read
		"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATH", "PATHEXT", "OS",
		"PROCESSOR_ARCHITECTURE", "PROCESSOR_ARCHITEW6432", "PROCESSOR_IDENTIFIER", "PROCESSOR_LEVEL",
		"PROCESSOR_REVISION", "NUMBER_OF_PROCESSORS",
		"ProgramData", "ALLUSERSPROFILE", "PUBLIC", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432",
		"CommonProgramFiles", "CommonProgramFiles(x86)", "CommonProgramW6432", "DriverData",
		// the user and the session
		"TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
		"USERNAME", "USERDOMAIN", "USERDOMAIN_ROAMINGPROFILE", "LOGONSERVER", "COMPUTERNAME", "SESSIONNAME",
	}
	// the helper's own test switch (its start-up wait for the tray host)
	helperEnvOwn = []string{"AI_USAGE_TRAY_WAIT"}
)

// envName is the variable's name in "NAME=value". On Windows the
// per-drive current folders are named "=C:".
func envName(kv string) (string, bool) {
	i := strings.IndexByte(kv[min(1, len(kv)):], '=')
	if i < 0 {
		return "", false
	}
	return kv[:i+min(1, len(kv))], true
}

// helperEnviron is the part of environ a helper on goos gets, plus the
// variables named in keep (tests).
func helperEnviron(goos string, environ, keep []string) []string {
	var names, prefixes []string
	switch goos {
	case "windows":
		names, prefixes = helperEnvWindows, []string{"="}
	case "darwin":
		names, prefixes = helperEnvDarwin, []string{"LC_"}
	default:
		names, prefixes = helperEnvLinux, []string{"XDG_", "LC_"}
	}
	same := func(a, b string) bool { return a == b }
	if goos == "windows" {
		same = strings.EqualFold
	}
	allowed := func(k string) bool {
		for _, list := range [][]string{names, helperEnvOwn, keep} {
			for _, n := range list {
				if same(k, n) {
					return true
				}
			}
		}
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, kv := range environ {
		if k, ok := envName(kv); ok && k != trayproto.EnvToken && allowed(k) {
			out = append(out, kv)
		}
	}
	return out
}
