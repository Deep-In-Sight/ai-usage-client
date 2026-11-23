package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const wantDesktop = `[Desktop Entry]
Type=Application
Name=Claude Usage
Comment=Shows Claude plan usage and reports it to your team's server
Exec=/h/a/.local/bin/claude-usage run
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
`

func TestDesktopText(t *testing.T) {
	eq(t, DesktopText("/h/a/.local/bin/claude-usage"), wantDesktop)
}

func TestDesktopTextQuoting(t *testing.T) {
	got := DesktopText("/h/a b/$x%/.local/bin/claude-usage")
	want := `Exec="/h/a b/\\$x%%/.local/bin/claude-usage" run`
	if !containsLine(got, want) {
		t.Fatalf("%s", got)
	}
	got = DesktopText("/h/q\"`\\/claude-usage")
	want = `Exec="/h/q\\"\\` + "`" + `\\\\/claude-usage" run`
	if !containsLine(got, want) {
		t.Fatalf("%s\nwant %s", got, want)
	}
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestXDGRegisterToggleUnregister(t *testing.T) {
	home := t.TempDir()
	var started [][]string
	x := &XDGAutostart{Home: home, Start: func(exe string, args ...string) error {
		started = append(started, append([]string{exe}, args...))
		return nil
	}}
	path := filepath.Join(home, ".config", "autostart", "claude-usage.desktop")
	eq(t, x.Name(), "xdg-autostart")
	eq(t, x.Available(bg), true)
	eq(t, x.State(bg), "not registered")
	must(t, x.Register(bg, "/h/a/.local/bin/claude-usage"))
	eq(t, started, [][]string{{"/h/a/.local/bin/claude-usage", "run"}})
	b, err := os.ReadFile(path)
	must(t, err)
	eq(t, string(b), wantDesktop)
	st, _ := os.Stat(path)
	eq(t, st.Mode().Perm(), os.FileMode(0o644))
	eq(t, x.State(bg), "registered")
	eq(t, x.Enabled(bg), true)

	must(t, x.SetEnabled(bg, "/h/a/.local/bin/claude-usage", false))
	eq(t, x.Enabled(bg), false)
	must(t, x.SetEnabled(bg, "/h/a/.local/bin/claude-usage", true))
	eq(t, x.Enabled(bg), true)
	eq(t, len(started), 1) // the switch starts nothing

	must(t, x.Unregister(bg))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file left")
	}
	must(t, x.Unregister(bg))

	x.Start = func(string, ...string) error { return errors.New("no exec") }
	if err := x.Register(bg, "/h/a/.local/bin/claude-usage"); !errors.Is(err, ErrStartFailed) {
		t.Fatalf("%v", err)
	}
}
