// Package autostart registers the agent to start with the user's session,
// using the one documented mechanism of each OS. This version has the
// systemd user service (Linux); the tray plan adds the others behind the
// same Method interface.
package autostart

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

const runTimeout = 20 * time.Second

// ExecRunner runs real commands, 20 s at most each.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), fmt.Errorf("%s: timed out after %s", name, runTimeout)
	}
	return string(out), err
}

// Method is one way to start the agent automatically.
type Method interface {
	Name() string
	Available(ctx context.Context) bool
	Register(ctx context.Context, exe string) error
	Stop(ctx context.Context) error
	Unregister(ctx context.Context) error
	State(ctx context.Context) string
}

// ErrUnsupported: no autostart method for this OS yet.
var ErrUnsupported = errors.New("autostart: not implemented on this OS yet")

// For picks the method for this OS. On Linux it is the systemd user service
// whether or not there is a desktop (the desktop autostart comes with the
// tray).
func For(headless bool, home string, run Runner) (Method, error) {
	return forOS(headless, home, run)
}

// UnitName is the systemd user unit.
const UnitName = "claude-usage.service"

// UnitText is the unit file for exe. "%" is doubled so systemd does not
// expand it as a specifier; ValidExePath rejects what cannot be quoted.
//
// After=network-online.target has no effect in a user manager; it is kept
// because the unit text is part of the shared plan, and the engine retries
// the network anyway.
func UnitText(exe string) string {
	exe = strings.ReplaceAll(exe, "%", "%%")
	return `[Unit]
Description=Claude Usage agent
After=network-online.target

[Service]
ExecStart="` + exe + `" run --headless
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
`
}

// ValidExePath refuses paths that cannot be written inside the quoted
// ExecStart= value.
func ValidExePath(exe string) error {
	if strings.ContainsAny(exe, "\"\\\n\r") {
		return fmt.Errorf("autostart: the install path %q cannot be used in a systemd unit", exe)
	}
	return nil
}

// Linger is whether the user's services keep running without a login.
type Linger string

const (
	LingerEnabled Linger = "enabled"
	LingerRefused Linger = "refused"
)

// EnsureLinger turns on linger for user if it is off. Without admin rights
// polkit may refuse; the detail then says why.
func EnsureLinger(ctx context.Context, run Runner, user string) (Linger, string) {
	isOn := func() bool {
		out, err := run(ctx, "loginctl", "show-user", user, "-p", "Linger", "--value")
		return err == nil && strings.TrimSpace(out) == "yes"
	}
	if isOn() {
		return LingerEnabled, ""
	}
	out, err := run(ctx, "loginctl", "enable-linger", "--no-ask-password", user)
	if isOn() {
		return LingerEnabled, ""
	}
	detail := strings.TrimSpace(out)
	if err != nil {
		if detail != "" {
			detail += ": "
		}
		detail += err.Error()
	}
	return LingerRefused, detail
}
