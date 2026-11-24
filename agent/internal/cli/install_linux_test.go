//go:build linux

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/osinfo"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
)

func (te *testEnv) unitPath() string {
	return filepath.Join(te.home, ".config", "systemd", "user", "ai-usage.service")
}

// systemdOK answers like a working systemd user manager, with linger as given.
func systemdOK(linger string, enableLinger error) func([]string) (string, error) {
	enabled := linger == "yes"
	return func(a []string) (string, error) {
		switch {
		case a[0] == "loginctl" && a[1] == "show-user":
			if enabled {
				return "yes\n", nil
			}
			return "no\n", nil
		case a[0] == "loginctl" && a[1] == "enable-linger":
			if enableLinger != nil {
				return "Could not enable linger: Access denied", enableLinger
			}
			enabled = true
			return "", nil
		case a[0] == "systemctl" && a[2] == "is-active":
			return "active\n", nil
		}
		return "", nil
	}
}

func TestInstallFresh(t *testing.T) {
	te := newEnv(t)
	te.vars["CLAUDE_CONFIG_DIR"] = "/x/claude"
	te.runner.reply = systemdOK("yes", nil)
	code := te.main("install", "--config", writeCfg(t, validConfig), "--headless")
	eq(t, code, 0)
	eq(t, read(t, te.Dirs.InstallExe), "BIN")
	st, _ := os.Stat(te.Dirs.InstallExe)
	eq(t, st.Mode().Perm(), os.FileMode(0o755))
	cfg, err := config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, cfg.ClaudeConfigDir, "/x/claude")
	eq(t, cfg.IngestKey, "test-ingest-key-0123456789")
	out := te.out.String()
	contains(t, out, "installed: "+te.Dirs.InstallExe)
	contains(t, out, "config: "+te.Dirs.File(paths.ConfigFile))
	contains(t, out, "autostart: systemd-user (active)")
	contains(t, out, "Claude Code sign-in: not found yet (the agent starts collecting once you sign in)")
	contains(t, read(t, te.unitPath()), `ExecStart="`+te.Dirs.InstallExe+`" run --headless`)
	if !slices.ContainsFunc(te.runner.calls, func(c []string) bool {
		return slices.Equal(c, []string{"systemctl", "--user", "enable", "--now", "ai-usage.service"})
	}) {
		t.Fatalf("calls %v", te.runner.calls)
	}
	if strings.Contains(out+te.err.String(), "test-ingest-key") {
		t.Fatal("ingest key printed")
	}
	eq(t, len(te.detached), 0)
}

func TestInstallSeesSignIn(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	dir := filepath.Join(te.home, ".claude")
	must(t, os.MkdirAll(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"fake-access-token-0123456789","expiresAt":4102444800000}}`), 0o600))
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	contains(t, te.out.String(), "Claude Code sign-in: found")
	if strings.Contains(te.out.String()+te.err.String(), "fake-access-token") {
		t.Fatal("token printed")
	}
}

func TestInstallLingerRefusedExits10(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("no", errors.New("exit status 1"))
	code := te.main("install", "--config", writeCfg(t, validConfig), "--headless")
	eq(t, code, 10)
	user := osinfo.Current().OSUser
	contains(t, te.out.String()+te.err.String(), "loginctl enable-linger "+user)
	contains(t, te.out.String()+te.err.String(), "linger is not enabled")
	if _, err := os.Stat(te.unitPath()); err != nil {
		t.Fatal("service not registered")
	}
}

func TestInstallDesktopSkipsLinger(t *testing.T) {
	te := newEnv(t)
	te.vars["XDG_SESSION_TYPE"] = "wayland"
	te.vars["XDG_CURRENT_DESKTOP"] = "ubuntu:GNOME"
	te.runner.reply = systemdOK("no", errors.New("exit status 1"))
	eq(t, te.main("install", "--config", writeCfg(t, validConfig)), 0)
	for _, c := range te.runner.calls {
		if c[0] == "loginctl" {
			t.Fatalf("linger touched on a desktop: %v", c)
		}
	}
}

func TestInstallWithoutSystemdStartsDetached(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = func(a []string) (string, error) {
		if a[0] == "systemctl" {
			return "Failed to connect to bus: No medium found", errors.New("exit status 1")
		}
		return "yes\n", nil
	}
	code := te.main("install", "--config", writeCfg(t, validConfig), "--headless")
	eq(t, code, 10)
	eq(t, te.detached, [][]string{{te.Dirs.InstallExe, "run", "--headless"}})
	contains(t, te.out.String()+te.err.String(), "autostart could not be registered; the agent runs until you log out or restart")
	eq(t, read(t, te.Dirs.InstallExe), "BIN")
}

func TestInstallBadConfigChangesNothing(t *testing.T) {
	te := newEnv(t)
	code := te.main("install", "--config", writeCfg(t, `{"server_urls":["http://x"],"ingest_key":""}`), "--headless")
	eq(t, code, 1)
	if _, err := os.Stat(te.Dirs.InstallExe); !os.IsNotExist(err) {
		t.Fatal("exe installed")
	}
	if _, err := os.Stat(te.unitPath()); !os.IsNotExist(err) {
		t.Fatal("unit written")
	}
	if _, err := os.Stat(te.Dirs.Data); !os.IsNotExist(err) {
		t.Fatal("data folder created")
	}
	eq(t, len(te.runner.calls), 0)
	code = te.main("install", "--config", filepath.Join(t.TempDir(), "missing.json"))
	eq(t, code, 1)
	eq(t, len(te.runner.calls), 0)
}

func TestInstallTwiceKeepsDeviceID(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	cfgPath := writeCfg(t, validConfig)
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	id, err := config.DeviceID(te.Dirs.File(paths.DeviceIDFile))
	must(t, err)
	must(t, os.WriteFile(te.Dirs.File(paths.BacklogFile), []byte(`[{"account_uuid":"acc-1","fetched_at":"2026-01-05T08:00:00.000Z","http_status":200,"usage":null}]`), 0o600))

	// the new version
	must(t, os.WriteFile(te.Exe, []byte("BIN2"), 0o755))
	var atStop []string
	inner := te.runner.reply
	te.runner.reply = func(a []string) (string, error) {
		if a[0] == "systemctl" && a[2] == "stop" {
			atStop = append(atStop, read(t, te.Dirs.InstallExe))
		}
		return inner(a)
	}
	te.runner.calls = nil
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	eq(t, atStop, []string{"BIN"}) // stopped before the copy
	eq(t, read(t, te.Dirs.InstallExe), "BIN2")
	id2, _ := config.DeviceID(te.Dirs.File(paths.DeviceIDFile))
	eq(t, id2, id)
	contains(t, read(t, te.Dirs.File(paths.BacklogFile)), "acc-1")
	if !slices.ContainsFunc(te.runner.calls, func(c []string) bool {
		return slices.Equal(c, []string{"systemctl", "--user", "restart", "ai-usage.service"})
	}) {
		t.Fatalf("no restart: %v", te.runner.calls)
	}
}

func TestInstallWithoutClaudeConfigDirEnvDropsKey(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	abs := filepath.Join(t.TempDir(), "claude")
	cfg := `{"server_urls":["http://ai-usage.local"],"ingest_key":"k","claude_config_dir":"` + abs + `"}`
	eq(t, te.main("install", "--config", writeCfg(t, cfg), "--headless"), 0)
	c, err := config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, c.ClaudeConfigDir, "")
	contains(t, read(t, te.Dirs.File(paths.ConfigFile)), `"server_urls"`)
	if strings.Contains(read(t, te.Dirs.File(paths.ConfigFile)), "claude_config_dir") {
		t.Fatal("key kept")
	}
}

func TestInstallRelativeClaudeConfigDirBecomesAbsolute(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	te.vars["CLAUDE_CONFIG_DIR"] = "rel/claude"
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	c, _ := config.Load(te.Dirs.File(paths.ConfigFile))
	if !filepath.IsAbs(c.ClaudeConfigDir) || !strings.HasSuffix(c.ClaudeConfigDir, "/rel/claude") {
		t.Fatal(c.ClaudeConfigDir)
	}
}

func TestNoArgsWithAdjacentConfigInstalls(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	must(t, os.WriteFile(filepath.Join(filepath.Dir(te.Exe), "config.json"), []byte(validConfig), 0o600))
	eq(t, te.main(), 0)
	eq(t, read(t, te.Dirs.InstallExe), "BIN")
	contains(t, te.out.String(), "installed: "+te.Dirs.InstallExe)

	// the installed executable itself, run without arguments, only prints usage
	te2 := newEnv(t)
	te2.Dirs = te.Dirs
	te2.Exe = te.Dirs.InstallExe
	must(t, os.WriteFile(filepath.Join(filepath.Dir(te2.Exe), "config.json"), []byte(validConfig), 0o600))
	eq(t, te2.main(), 2)
}

func TestUninstallRemovesEverything(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	_, _ = config.DeviceID(te.Dirs.File(paths.DeviceIDFile))
	te.runner.calls = nil
	te.out.Reset()
	eq(t, te.main("uninstall"), 0)
	contains(t, te.out.String(), "removed")
	if _, err := os.Stat(te.Dirs.InstallExe); !os.IsNotExist(err) {
		t.Fatal("exe still there")
	}
	if _, err := os.Stat(te.Dirs.Data); !os.IsNotExist(err) {
		t.Fatal("data still there")
	}
	if _, err := os.Stat(te.unitPath()); !os.IsNotExist(err) {
		t.Fatal("unit still there")
	}
	saw := func(want ...string) bool {
		return slices.ContainsFunc(te.runner.calls, func(c []string) bool { return slices.Equal(c, want) })
	}
	if !saw("systemctl", "--user", "stop", "ai-usage.service") || !saw("systemctl", "--user", "disable", "--now", "ai-usage.service") {
		t.Fatalf("calls %v", te.runner.calls)
	}
	for _, c := range te.runner.calls {
		if c[0] == "loginctl" {
			t.Fatal("uninstall touched linger")
		}
	}
	// a second uninstall is harmless
	eq(t, te.main("uninstall"), 0)
}

func TestInstallWithoutUserManagerTriesLingerFirst(t *testing.T) {
	// e.g. run from "su -": no user manager until linger starts one
	te := newEnv(t)
	lingered := false
	te.runner.reply = func(a []string) (string, error) {
		switch {
		case a[0] == "loginctl" && a[1] == "show-user":
			if lingered {
				return "yes\n", nil
			}
			return "no\n", nil
		case a[0] == "loginctl" && a[1] == "enable-linger":
			lingered = true
			return "", nil
		case a[0] == "systemctl" && !lingered:
			return "Failed to connect to bus", errors.New("exit status 1")
		case a[0] == "systemctl" && a[2] == "is-active":
			return "active\n", nil
		}
		return "", nil
	}
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	eq(t, len(te.detached), 0)
	contains(t, te.out.String(), "autostart: systemd-user (active)")
}

func TestInstallCopyFailureLeavesOldAgentRunning(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	must(t, os.Remove(te.Exe)) // the new executable cannot be read
	te.runner.calls = nil
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 1)
	for _, c := range te.runner.calls {
		if c[0] == "systemctl" && (c[2] == "stop" || c[2] == "disable") {
			t.Fatalf("stopped although nothing could be installed: %v", c)
		}
	}
	eq(t, read(t, te.Dirs.InstallExe), "BIN")
}

func TestInstallFailureAfterStopRestartsOldAgent(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	// the config cannot be written: the data folder became read-only
	must(t, os.Chmod(te.Dirs.Data, 0o500))
	t.Cleanup(func() { _ = os.Chmod(te.Dirs.Data, 0o700) })
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	must(t, os.WriteFile(te.Exe, []byte("BIN2"), 0o755))
	te.runner.calls = nil
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 1)
	stopped, restarted := -1, -1
	for i, c := range te.runner.calls {
		if slices.Equal(c, []string{"systemctl", "--user", "stop", "ai-usage.service"}) {
			stopped = i
		}
		if slices.Equal(c, []string{"systemctl", "--user", "restart", "ai-usage.service"}) {
			restarted = i
		}
	}
	if stopped < 0 || restarted < stopped {
		t.Fatalf("old agent not started again: %v", te.runner.calls)
	}
}

// install records the Codex folder of the installing shell (CODEX_HOME)
// and the codex it finds there, resolved to the real binary (design 6.12,
// D15); an upgrade from a shell without them keeps both.
func TestInstallRecordsCodexPaths(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	te.vars["CODEX_HOME"] = codexHome
	real := filepath.Join(t.TempDir(), "codex-0.172.0")
	must(t, os.WriteFile(real, []byte("\x7fELF fake"), 0o755))
	bin := t.TempDir()
	must(t, os.Symlink(real, filepath.Join(bin, "codex")))
	te.vars["PATH"] = filepath.Join(t.TempDir(), "empty") + string(os.PathListSeparator) + bin
	cfgPath := writeCfg(t, validConfig)
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	c, err := config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, c.CodexHome, codexHome)
	eq(t, c.CodexPath, real)
	out := te.out.String()
	contains(t, out, "Codex folder: "+codexHome+"\n")
	if strings.Contains(out, "Codex CLI") { // recorded, never used by a released agent: not shown (ruling F3)
		t.Fatalf("install names the Codex CLI:\n%s", out)
	}
	if strings.Contains(out, "Codex sign-in:") {
		t.Fatal("a Codex sign-in line without a sign-in")
	}

	// upgraded from a shell without CODEX_HOME and without codex on PATH
	delete(te.vars, "CODEX_HOME")
	te.vars["PATH"] = filepath.Join(t.TempDir(), "empty")
	writeFile(t, filepath.Join(codexHome, "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-fake-0123456789abcdef"}`)
	te.out.Reset()
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	c, err = config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, c.CodexHome, codexHome)
	eq(t, c.CodexPath, real)
	out = te.out.String()
	contains(t, out, "Codex folder: "+codexHome+" (kept from the previous install; set CODEX_HOME to change it)")
	contains(t, out, "Codex sign-in: an API key (no plan limits)")
	if strings.Contains(out, "sk-fake") {
		t.Fatal("the key was printed")
	}

	// a recorded codex that is gone is dropped; a fresh install without
	// either records neither
	must(t, os.Remove(real))
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	c, _ = config.Load(te.Dirs.File(paths.ConfigFile))
	eq(t, c.CodexPath, "")
	te2 := newEnv(t)
	te2.runner.reply = systemdOK("yes", nil)
	eq(t, te2.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	c, _ = config.Load(te2.Dirs.File(paths.ConfigFile))
	eq(t, c.CodexHome+c.CodexPath, "")
	if strings.Contains(te2.out.String(), "\nCodex ") {
		t.Fatalf("Codex lines without Codex:\n%s", te2.out.String())
	}
}

// Review m-4: an install from a shell with a relative PATH entry
// (./node_modules/.bin) inside some repository records nothing from it,
// and a recorded codex_path that is not absolute is not kept.
func TestInstallRecordsNoRelativeCodexPath(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	repo := t.TempDir()
	t.Chdir(repo)
	writeFile(t, filepath.Join(repo, "node_modules", ".bin", "codex"), "\x7fELF fake")
	must(t, os.Chmod(filepath.Join(repo, "node_modules", ".bin", "codex"), 0o755))
	te.vars["PATH"] = "./node_modules/.bin" + string(os.PathListSeparator) + filepath.Join(t.TempDir(), "empty")
	cfgPath := writeCfg(t, validConfig)
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	c, err := config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, c.CodexPath, "")
	// an older install recorded a relative path: dropped
	c.CodexPath = filepath.Join("node_modules", ".bin", "codex")
	b, err := json.Marshal(c)
	must(t, err)
	must(t, os.WriteFile(te.Dirs.File(paths.ConfigFile), b, 0o600))
	te.vars["PATH"] = filepath.Join(t.TempDir(), "empty")
	eq(t, te.main("install", "--config", cfgPath, "--headless"), 0)
	c, err = config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, c.CodexPath, "")
}

// An npm-installed codex is recorded as the binary it runs, never the
// node launcher.
func TestInstallRecordsTheBinaryBehindAnNpmShim(t *testing.T) {
	te := newEnv(t)
	te.runner.reply = systemdOK("yes", nil)
	prefix := t.TempDir()
	pkg := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex")
	writeFile(t, filepath.Join(pkg, "bin", "codex.js"), "#!/usr/bin/env node\n")
	triple := map[string]string{"amd64": "x86_64-unknown-linux-musl", "arm64": "aarch64-unknown-linux-musl"}[runtime.GOARCH]
	native := filepath.Join(pkg, "vendor", triple, "bin", "codex")
	writeFile(t, native, "\x7fELF fake")
	must(t, os.Chmod(native, 0o755))
	must(t, os.MkdirAll(filepath.Join(prefix, "bin"), 0o700))
	must(t, os.Symlink(filepath.Join("..", "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), filepath.Join(prefix, "bin", "codex")))
	te.vars["PATH"] = filepath.Join(prefix, "bin")
	eq(t, te.main("install", "--config", writeCfg(t, validConfig), "--headless"), 0)
	c, err := config.Load(te.Dirs.File(paths.ConfigFile))
	must(t, err)
	eq(t, c.CodexPath, native)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(body), 0o600))
}
