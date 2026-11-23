package autostart

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// Label is the LaunchAgent's label.
const Label = "io.github.deep-in-sight.claude-usage"

// LaunchAgent starts the agent at login on macOS. It has no KeepAlive: like
// the Run key and XDG autostart, a second "run" that finds the agent already
// running exits, and launchd must not restart it over and over.
type LaunchAgent struct {
	Home    string
	LogPath string
	UID     int
	Exe     string // for finding a process left running after bootout
	Run     Runner
}

func (l *LaunchAgent) Name() string { return "launchagent" }

// PlistPath is ~/Library/LaunchAgents/<Label>.plist.
func (l *LaunchAgent) PlistPath() string {
	return filepath.Join(l.Home, "Library", "LaunchAgents", Label+".plist")
}

func (l *LaunchAgent) domain() string  { return fmt.Sprintf("gui/%d", l.UID) }
func (l *LaunchAgent) service() string { return l.domain() + "/" + Label }

// PlistText is the property list for exe, logging to logPath.
func PlistText(exe, logPath string) string {
	e, lp := html.EscapeString(exe), html.EscapeString(logPath)
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key><array><string>` + e + `</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>` + lp + `</string>
  <key>StandardErrorPath</key><string>` + lp + `</string>
</dict>
</plist>
`
}

func (l *LaunchAgent) Available(context.Context) bool { return true }

// Preflight checks that ~/Library/LaunchAgents is writable.
func (l *LaunchAgent) Preflight(context.Context) error { return probeDir(filepath.Dir(l.PlistPath())) }

func (l *LaunchAgent) writePlist(exe string) error {
	if err := os.MkdirAll(filepath.Dir(l.PlistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(l.PlistPath(), []byte(PlistText(exe, l.LogPath)), 0o644); err != nil {
		return err
	}
	return os.Chmod(l.PlistPath(), 0o644)
}

func (l *LaunchAgent) launchctl(ctx context.Context, args ...string) (string, error) {
	out, err := l.Run(ctx, "launchctl", args...)
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return out, fmt.Errorf("launchctl %s: %v: %s", args[0], err, msg)
	}
	return out, nil
}

// Register writes the plist and (re)loads it, which starts the agent. A
// failed bootstrap leaves the plist for the next login and returns
// ErrStartFailed.
func (l *LaunchAgent) Register(ctx context.Context, exe string) error {
	if err := l.writePlist(exe); err != nil {
		return err
	}
	_, _ = l.launchctl(ctx, "bootout", l.service())
	if _, err := l.launchctl(ctx, "bootstrap", l.domain(), l.PlistPath()); err != nil {
		return fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	return nil
}

// Stop unloads the agent (which ends it) and ends any copy left running.
func (l *LaunchAgent) Stop(ctx context.Context) error {
	_, _ = l.launchctl(ctx, "bootout", l.service())
	killLeft(l.Exe)
	return nil
}

// Unregister unloads the agent and removes the plist.
func (l *LaunchAgent) Unregister(ctx context.Context) error {
	_, _ = l.launchctl(ctx, "bootout", l.service())
	if err := os.Remove(l.PlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// State is "running", another launchd state, "not loaded" or
// "not registered".
func (l *LaunchAgent) State(ctx context.Context) string {
	if _, err := os.Stat(l.PlistPath()); err != nil {
		return "not registered"
	}
	out, err := l.launchctl(ctx, "print", l.service())
	if err != nil {
		return "not loaded"
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
			return v
		}
	}
	return "loaded"
}

// Enabled: the plist is there.
func (l *LaunchAgent) Enabled(context.Context) bool {
	_, err := os.Stat(l.PlistPath())
	return err == nil
}

// SetEnabled writes or removes the plist only; what runs now keeps running
// until logout.
func (l *LaunchAgent) SetEnabled(_ context.Context, exe string, on bool) error {
	if on {
		return l.writePlist(exe)
	}
	if err := os.Remove(l.PlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
