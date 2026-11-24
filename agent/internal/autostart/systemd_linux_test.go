//go:build linux

package autostart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wantUnit = `[Unit]
Description=AI Usage agent
After=network-online.target

[Service]
ExecStart="/h/.local/bin/ai-usage" run --headless
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
`

func TestUnitText(t *testing.T) {
	eq(t, UnitText("/h/.local/bin/ai-usage"), wantUnit)
}

func TestForPicksMethod(t *testing.T) {
	o := Options{Headless: true, Home: t.TempDir(), Run: (&fakeRun{}).run}
	m, err := For(o)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, m.Name(), "systemd-user")
	eq(t, names(Others(o)), []string{"xdg-autostart"})
	o.Headless = false
	m, err = For(o)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, m.Name(), "xdg-autostart")
	eq(t, names(Others(o)), []string{"systemd-user"})
	if _, ok := m.(Toggler); !ok {
		t.Fatal("xdg-autostart is not a Toggler")
	}
}

func names(ms []Method) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Name())
	}
	return out
}

func TestRegisterCallsSystemctl(t *testing.T) {
	home := t.TempDir()
	f := &fakeRun{}
	m, _ := For(Options{Headless: true, Home: home, Run: f.run})
	exe := filepath.Join(home, ".local", "bin", "ai-usage")
	if err := m.Register(context.Background(), exe); err != nil {
		t.Fatal(err)
	}
	eq(t, f.calls, [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "ai-usage.service"},
	})
	b, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "ai-usage.service"))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, string(b), UnitText(exe))

	// registering again (an upgrade) restarts the running service
	f.calls = nil
	if err := m.Register(context.Background(), exe); err != nil {
		t.Fatal(err)
	}
	eq(t, f.calls, [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "ai-usage.service"},
		{"systemctl", "--user", "restart", "ai-usage.service"},
	})
}

func TestRegisterFailure(t *testing.T) {
	f := &fakeRun{reply: func(a []string) (string, error) {
		if a[2] == "enable" {
			return "Failed to connect to bus", errors.New("exit status 1")
		}
		return "", nil
	}}
	m, _ := For(Options{Headless: true, Home: t.TempDir(), Run: f.run})
	err := m.Register(context.Background(), "/x/ai-usage")
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("%v", err)
	}
}

func TestAvailableStopStateUnregister(t *testing.T) {
	home := t.TempDir()
	f := &fakeRun{reply: func(a []string) (string, error) {
		switch a[2] {
		case "show-environment":
			return "HOME=/h\n", nil
		case "is-active":
			return "active\n", nil
		}
		return "", nil
	}}
	m, _ := For(Options{Headless: true, Home: home, Run: f.run})
	ctx := context.Background()
	eq(t, m.Available(ctx), true)
	// nothing installed: Stop is a no-op, State says so
	f.calls = nil
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	eq(t, len(f.calls), 0)
	eq(t, m.State(ctx), "not installed")

	if err := m.Register(ctx, "/x/ai-usage"); err != nil {
		t.Fatal(err)
	}
	eq(t, m.State(ctx), "active")
	f.calls = nil
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	eq(t, f.calls, [][]string{{"systemctl", "--user", "stop", "ai-usage.service"}})
	f.calls = nil
	if err := m.Unregister(ctx); err != nil {
		t.Fatal(err)
	}
	eq(t, f.calls, [][]string{
		{"systemctl", "--user", "disable", "--now", "ai-usage.service"},
		{"systemctl", "--user", "daemon-reload"},
	})
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "ai-usage.service")); !os.IsNotExist(err) {
		t.Fatal("unit file still there")
	}

	f.reply = func([]string) (string, error) { return "Failed to connect to bus", errors.New("exit status 1") }
	eq(t, m.Available(ctx), false)
}

func TestEnsureLinger(t *testing.T) {
	ctx := context.Background()
	// already enabled
	f := &fakeRun{reply: func(a []string) (string, error) { return "yes\n", nil }}
	l, _ := EnsureLinger(ctx, f.run, "user-a")
	eq(t, l, LingerEnabled)
	eq(t, f.calls, [][]string{{"loginctl", "show-user", "user-a", "-p", "Linger", "--value"}})

	// enabled by us
	enabled := false
	f = &fakeRun{reply: func(a []string) (string, error) {
		if a[1] == "enable-linger" {
			enabled = true
			return "", nil
		}
		if enabled {
			return "yes\n", nil
		}
		return "no\n", nil
	}}
	l, _ = EnsureLinger(ctx, f.run, "user-a")
	eq(t, l, LingerEnabled)
	eq(t, f.calls[1], []string{"loginctl", "enable-linger", "--no-ask-password", "user-a"})
	eq(t, len(f.calls), 3)

	// refused
	f = &fakeRun{reply: func(a []string) (string, error) {
		if a[1] == "enable-linger" {
			return "Interactive authentication required.", errors.New("exit status 1")
		}
		return "no\n", nil
	}}
	l, detail := EnsureLinger(ctx, f.run, "user-a")
	eq(t, l, LingerRefused)
	if !strings.Contains(detail, "Interactive authentication required") {
		t.Fatal(detail)
	}
}

func TestUnitEscapesSpecifiers(t *testing.T) {
	if !strings.Contains(UnitText("/h/50%/ai-usage"), `ExecStart="/h/50%%/ai-usage" run --headless`) {
		t.Fatal(UnitText("/h/50%/ai-usage"))
	}
	for _, exe := range []string{`/h/a"b/ai-usage`, `/h/a\b/ai-usage`, "/h/a\nb/ai-usage"} {
		f := &fakeRun{}
		home := t.TempDir()
		m, _ := For(Options{Headless: true, Home: home, Run: f.run})
		if err := m.Register(context.Background(), exe); err == nil {
			t.Fatalf("%q accepted", exe)
		}
		eq(t, len(f.calls), 0)
		if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", UnitName)); !os.IsNotExist(err) {
			t.Fatal("unit written")
		}
	}
}
