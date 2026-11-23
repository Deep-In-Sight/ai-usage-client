package autostart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func forOS(_ bool, home string, run Runner) (Method, error) {
	return &Systemd{home: home, run: run}, nil
}

// Systemd is the systemd user service. The unit lives in
// ~/.config/systemd/user regardless of XDG_CONFIG_HOME, like the data folder.
type Systemd struct {
	home string
	run  Runner
}

func (s *Systemd) Name() string { return "systemd-user" }

func (s *Systemd) unitPath() string {
	return filepath.Join(s.home, ".config", "systemd", "user", UnitName)
}

func (s *Systemd) systemctl(ctx context.Context, args ...string) error {
	out, err := s.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

// Available reports whether a systemd user manager answers.
func (s *Systemd) Available(ctx context.Context) bool {
	return s.systemctl(ctx, "show-environment") == nil
}

func (s *Systemd) installed() bool {
	_, err := os.Stat(s.unitPath())
	return err == nil
}

// Register writes the unit, enables it and starts it (restarts it when it
// was already there, e.g. on an upgrade).
func (s *Systemd) Register(ctx context.Context, exe string) error {
	existed := s.installed()
	if err := os.MkdirAll(filepath.Dir(s.unitPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.unitPath(), []byte(UnitText(exe)), 0o644); err != nil {
		return err
	}
	if err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if !existed {
		return s.systemctl(ctx, "enable", "--now", UnitName)
	}
	if err := s.systemctl(ctx, "enable", UnitName); err != nil {
		return err
	}
	return s.systemctl(ctx, "restart", UnitName)
}

// Stop stops the service; no unit is not an error.
func (s *Systemd) Stop(ctx context.Context) error {
	if !s.installed() {
		return nil
	}
	return s.systemctl(ctx, "stop", UnitName)
}

// Unregister disables and removes the unit.
func (s *Systemd) Unregister(ctx context.Context) error {
	if !s.installed() {
		return nil
	}
	errDisable := s.systemctl(ctx, "disable", "--now", UnitName)
	if err := os.Remove(s.unitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = s.systemctl(ctx, "daemon-reload")
	return errDisable
}

// State is systemctl is-active's answer, or "not installed".
func (s *Systemd) State(ctx context.Context) string {
	if !s.installed() {
		return "not installed"
	}
	out, _ := s.run(ctx, "systemctl", "--user", "is-active", UnitName)
	if st := strings.TrimSpace(out); st != "" && !strings.ContainsAny(st, " \n") {
		return st
	}
	return "unknown"
}
