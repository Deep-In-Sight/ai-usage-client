package autostart

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// RunKey starts the agent at logon through HKCU\...\Run.
type RunKey struct {
	ValueName string // "" means "ClaudeUsage"
	Exe       string // for finding a process left running
	Start     func(exe string, args ...string) error
}

func (r *RunKey) Name() string { return "hkcu-run" }

func (r *RunKey) value() string {
	if r.ValueName == "" {
		return "ClaudeUsage"
	}
	return r.ValueName
}

func (r *RunKey) Available(context.Context) bool { return true }

func (r *RunKey) write(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("autostart: Run key: %w", err)
	}
	defer k.Close()
	return k.SetStringValue(r.value(), RunValue(exe))
}

// Register writes the Run value and starts the agent now.
func (r *RunKey) Register(_ context.Context, exe string) error {
	if err := r.write(exe); err != nil {
		return err
	}
	if r.Start == nil {
		return fmt.Errorf("%w: no way to start a process", ErrStartFailed)
	}
	if err := r.Start(exe, "run"); err != nil {
		return fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	return nil
}

// Stop ends the running agent.
func (r *RunKey) Stop(context.Context) error {
	killLeft(r.Exe)
	return nil
}

// Unregister deletes the Run value; no value is not an error.
func (r *RunKey) Unregister(context.Context) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(r.value()); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func (r *RunKey) State(ctx context.Context) string {
	if r.Enabled(ctx) {
		return "registered"
	}
	return "not registered"
}

func (r *RunKey) Enabled(context.Context) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(r.value())
	return err == nil
}

// SetEnabled writes or deletes the Run value only.
func (r *RunKey) SetEnabled(ctx context.Context, exe string, on bool) error {
	if on {
		return r.write(exe)
	}
	return r.Unregister(ctx)
}
