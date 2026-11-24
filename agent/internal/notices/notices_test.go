package notices

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func goCmd(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %v: %v", args, err)
	}
	return string(out)
}

// compiledModules are the modules the release binaries contain, for every
// OS the release builds (macOS with cgo: the tray).
func compiledModules(t *testing.T) []string {
	t.Helper()
	var mods []string
	for _, env := range [][]string{
		{"GOOS=linux", "CGO_ENABLED=0"},
		{"GOOS=windows", "CGO_ENABLED=0"},
		{"GOOS=darwin", "CGO_ENABLED=1"},
	} {
		out := goCmd(t, env, "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}", "./cmd/ai-usage")
		for _, m := range strings.Fields(out) {
			if !slices.Contains(mods, m) {
				mods = append(mods, m)
			}
		}
	}
	slices.Sort(mods)
	return mods
}

// Every module compiled into a release binary has its notice, and nothing
// else is listed.
func TestEveryCompiledModuleHasANotice(t *testing.T) {
	var listed []string
	for _, m := range Modules {
		if m.Path != GoPath {
			listed = append(listed, m.Path)
		}
	}
	slices.Sort(listed)
	if got := compiledModules(t); !slices.Equal(got, listed) {
		t.Fatalf("compiled modules %v, notices for %v", got, listed)
	}
}

// The embedded files are the modules' own licence files, unchanged, and
// every licence-like file a module ships is embedded.
func TestNoticesAreTheModulesFiles(t *testing.T) {
	for _, m := range Modules {
		dir := strings.TrimSpace(goCmd(t, nil, "env", "GOROOT"))
		if m.Path != GoPath {
			dir = strings.TrimSpace(goCmd(t, nil, "list", "-m", "-f", "{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}", m.Path))
			if !filepath.IsAbs(dir) {
				dir = filepath.Join("../..", dir)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, e := range entries {
			n := strings.ToUpper(e.Name())
			if !e.IsDir() && (strings.HasPrefix(n, "LICENSE") || strings.HasPrefix(n, "LICENCE") ||
				strings.HasPrefix(n, "COPYING") || strings.HasPrefix(n, "NOTICE") || strings.HasPrefix(n, "PATENTS")) {
				want = append(want, e.Name())
			}
		}
		if !slices.Equal(m.Files, want) {
			t.Fatalf("%s: embeds %v, the module has %v", m.Path, m.Files, want)
		}
		for _, f := range m.Files {
			src, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			got, err := files.ReadFile(m.embedded(f))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("%s %s differs from the module's file", m.Path, f)
			}
		}
	}
}

func TestTextNamesEveryModuleAndItsVersion(t *testing.T) {
	var b bytes.Buffer
	Write(&b, map[string]string{"fyne.io/systray": "v1.12.2", "golang.org/x/sys": "v0.50.0"}, "go1.27.4")
	s := b.String()
	for _, want := range []string{
		"Go go1.27.4", "fyne.io/systray v1.12.2", "golang.org/x/sys v0.50.0",
		"Apache License", "Version 2.0, January 2004", // systray's LICENSE text
		"modified", "PATENTS", "Clawd",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %q in the notices", want)
		}
	}
	// a module not in this binary (here: miekg/dns, absent from the map) is
	// still listed, without a version
	if !strings.Contains(s, "github.com/miekg/dns\n") {
		t.Fatal("miekg/dns missing")
	}
}

// Git must not convert the licence files' line endings, nor those of the
// systray copy they are compared with (core.autocrlf on Windows would make
// them differ from the modules' files).
func TestLicenceFilesKeepTheirBytesInGit(t *testing.T) {
	b, err := os.ReadFile("../../../.gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"agent/internal/notices/licenses/** -text", "agent/third_party/systray/** -text"} {
		if !lines[want] {
			t.Fatalf(".gitattributes has no line %q", want)
		}
	}
}
