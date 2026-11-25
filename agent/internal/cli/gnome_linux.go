//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/gnomeext"
)

// The usage panel's GNOME Shell extension at install and removal (design
// 6.6). Only this Linux file reaches gnomeext, so nothing of the extension
// is in the Windows and macOS executables (pre-flight ruling F16).

// panelBusTimeout bounds the whole session-bus exchange of install and
// removal (plan PR-14).
const panelBusTimeout = 2 * time.Second

// shellBus connects to the session bus at the address the helpers use
// (gnomeext.DialSession: never one that would start a bus, plan RF3),
// giving up when ctx ends.
func shellBus(ctx context.Context, getenv func(string) string) (*dbus.Conn, error) {
	type result struct {
		conn *dbus.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, err := gnomeext.DialSession(getenv)
		done <- result{c, err}
	}()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// installPanelExtension writes the extension for the GNOME Shell of the
// installing session when that shell answers on the session bus within
// panelBusTimeout and is one the panel supports; first reports that the
// extension's folder was not there before (install then says the panel
// comes after the next login). Anything else (no bus: SSH, "su -", before
// the first GNOME login; another desktop; a failure) writes nothing and
// says nothing: the bridge writes it at the next GNOME login (design 6.4).
func installPanelExtension(env Env) (first bool) {
	dir, err := gnomeext.Dir(env.Getenv, env.Dirs.Home)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), panelBusTimeout)
	defer cancel()
	conn, err := shellBus(ctx, env.Getenv)
	if err != nil {
		return false
	}
	major, _, err := gnomeext.ShellMajor(ctx, conn)
	conn.Close()
	if err != nil {
		return false
	}
	if _, ok := gnomeext.FormFor(major); !ok {
		return false
	}
	_, statErr := os.Lstat(dir)
	if _, err := gnomeext.Write(dir, major); err != nil { // never leaves a half-written folder
		return false
	}
	return errors.Is(statErr, os.ErrNotExist)
}

// removePanelExtension is the removal step remove-gnome-extension: with a
// session bus, the shell turns the extension off first (its disable()
// takes it out of the top bar; GNOME lists it in disabled-extensions);
// an error there is reported and the removal goes on (GNOME 46 once
// answered NoReply although the extension was off: stage 0, Z3). Then the
// extension's folder and staging leftovers are deleted, with or without
// a bus.
func removePanelExtension(env Env) error {
	dir, err := gnomeext.Dir(env.Getenv, env.Dirs.Home)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), panelBusTimeout)
	defer cancel()
	if conn, err := shellBus(ctx, env.Getenv); err == nil {
		_, err := gnomeext.Disable(ctx, conn, gnomeext.UUID)
		conn.Close()
		if err != nil && !noShell(err) {
			fmt.Fprintln(env.Stderr, "ai-usage uninstall: turning the panel extension off (removing it anyway):", err)
		}
	}
	return gnomeext.Remove(dir)
}

// noShell: the error says nobody answers for GNOME Shell's extensions on
// this bus (another desktop): nothing to report.
func noShell(err error) bool {
	var e dbus.Error
	return errors.As(err, &e) && (e.Name == "org.freedesktop.DBus.Error.ServiceUnknown" || e.Name == "org.freedesktop.DBus.Error.NameHasNoOwner")
}
