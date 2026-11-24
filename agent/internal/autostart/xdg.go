package autostart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// XDGAutostart starts the agent with a Linux desktop session through
// ~/.config/autostart (XDG_CONFIG_HOME is not looked at, like the data
// folder).
type XDGAutostart struct {
	Home  string
	Exe   string // for finding a process left running
	Start func(exe string, args ...string) error
}

func (x *XDGAutostart) Name() string { return "xdg-autostart" }

func (x *XDGAutostart) path() string {
	return filepath.Join(x.Home, ".config", "autostart", "ai-usage.desktop")
}

// execQuote quotes a path for a desktop entry's Exec key.
func execQuote(p string) string {
	plain := true
	for _, c := range p {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("/._+-", c)) {
			plain = false
			break
		}
	}
	p = strings.ReplaceAll(p, "%", "%%")
	if plain {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range p {
		switch c {
		case '"', '`', '$':
			b.WriteString(`\\`)
			b.WriteRune(c)
		case '\\':
			b.WriteString(`\\\\`)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// DesktopText is the autostart entry for exe. It is visible in the
// desktop's list of startup programs (no NoDisplay): nothing is hidden.
func DesktopText(exe string) string {
	return `[Desktop Entry]
Type=Application
Name=AI Usage
Comment=Shows Claude plan usage and reports it to your team's server
Exec=` + execQuote(exe) + ` run
Terminal=false
X-GNOME-Autostart-enabled=true
`
}

func (x *XDGAutostart) Available(context.Context) bool { return true }

// Preflight checks that ~/.config/autostart is writable.
func (x *XDGAutostart) Preflight(context.Context) error { return probeDir(filepath.Dir(x.path())) }

func (x *XDGAutostart) write(exe string) error {
	if strings.ContainsAny(exe, "\n\r") {
		return fmt.Errorf("autostart: the install path %q cannot be used in a desktop entry", exe)
	}
	if err := os.MkdirAll(filepath.Dir(x.path()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(x.path(), []byte(DesktopText(exe)), 0o644); err != nil {
		return err
	}
	return os.Chmod(x.path(), 0o644)
}

// Register writes the entry and starts the agent now.
func (x *XDGAutostart) Register(_ context.Context, exe string) error {
	if err := x.write(exe); err != nil {
		return err
	}
	if x.Start == nil {
		return fmt.Errorf("%w: no way to start a process", ErrStartFailed)
	}
	if err := x.Start(exe, "run"); err != nil {
		return fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	return nil
}

// Stop ends the running agent.
func (x *XDGAutostart) Stop(context.Context) error {
	killLeft(x.Exe)
	return nil
}

// Unregister removes the entry.
func (x *XDGAutostart) Unregister(context.Context) error {
	if err := os.Remove(x.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (x *XDGAutostart) State(context.Context) string {
	if x.Enabled(context.Background()) {
		return "registered"
	}
	return "not registered"
}

func (x *XDGAutostart) Enabled(context.Context) bool {
	_, err := os.Stat(x.path())
	return err == nil
}

// SetEnabled writes or removes the entry only.
func (x *XDGAutostart) SetEnabled(_ context.Context, exe string, on bool) error {
	if on {
		return x.write(exe)
	}
	return x.Unregister(context.Background())
}
