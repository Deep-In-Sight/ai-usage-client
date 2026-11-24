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
				strings.HasPrefix(n, "COPYING") || strings.HasPrefix(n, "COPYRIGHT") || strings.HasPrefix(n, "AUTHORS") ||
				strings.HasPrefix(n, "NOTICE") || strings.HasPrefix(n, "PATENTS")) {
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

// The program's own licence is the repository's LICENSE, unchanged.
func TestOwnLicenceIsTheRepositorysLicense(t *testing.T) {
	want, err := os.ReadFile("../../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	got, err := files.ReadFile(OwnLicence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the repository's LICENSE", OwnLicence)
	}
}

// The notices start with AI Usage's own licence and what it does not
// cover, before any third-party entry.
func TestTextStartsWithTheProgramsLicence(t *testing.T) {
	var b bytes.Buffer
	Write(&b, map[string]string{"fyne.io/systray": "v1.12.2"}, "go1.27.4")
	s := b.String()
	if !strings.HasPrefix(s, "AI Usage\n") {
		t.Fatalf("the notices start with %q", s[:min(len(s), 40)])
	}
	s = strings.Join(strings.Fields(s), " ") // the wording, not the line breaks
	first := strings.Index(s, "Go go1.27.4")
	if first < 0 {
		t.Fatal("no Go entry")
	}
	for _, want := range []string{
		"Licence: MIT License",
		"Copyright (c) 2026 Deep-In-Sight", "Permission is hereby granted", // LICENSE
		"AI Usage is an independent internal tool. It is not affiliated with, endorsed by, or sponsored by Anthropic PBC or OpenAI.",
		"The Clawd artwork, which the tray icon shows while Claude Code is signed in, depicts the Claude Code mascot, (c) Anthropic PBC. It is not covered by this licence, no licence from Anthropic is claimed, and it will be removed on request.",
		"(fyne.io/systray: Apache License 2.0, changes to its files included; files added by AI Usage: MIT)",
	} {
		i := strings.Index(s, want)
		if i < 0 || i > first {
			t.Fatalf("%q is not in the program's part of the notices", want)
		}
	}
	if !strings.Contains(s[:first], "not covered by this licence") {
		t.Fatal("the exceptions are not stated")
	}
}

// Every third-party mark gets the same three statements in the notices.
func TestEveryMarkHasItsNotice(t *testing.T) {
	if len(Marks) == 0 {
		t.Fatal("no marks: Clawd is in the tray icon")
	}
	var b bytes.Buffer
	Write(&b, nil, "go1.27.4")
	s := strings.Join(strings.Fields(b.String()), " ")
	first := strings.Index(s, "Go go1.27.4")
	for _, m := range Marks {
		want := "depicts " + m.Depicts + ", (c) " + m.Owner + ". It is not covered by this licence, no licence from " +
			m.Licensor + " is claimed, and it will be removed on request."
		if i := strings.Index(s, want); i < 0 || i > first {
			t.Fatalf("no %q before the third-party software", want)
		}
	}
}

// The README's "Third-party marks" section has the same notice for every
// mark, naming its files: Marks and that section are the one place a mark
// is added.
func TestReadmeListsEveryMark(t *testing.T) {
	b, err := os.ReadFile("../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n") // Windows checkouts have CRLF
	i := strings.Index(s, "\n### Third-party marks\n")
	if i < 0 {
		t.Fatal("README has no Third-party marks section")
	}
	s = s[i+1:]
	if j := strings.Index(s[4:], "\n#"); j >= 0 {
		s = s[:j+4]
	}
	s = strings.Join(strings.Fields(s), " ")
	for _, want := range []string{
		"AI Usage is an independent internal tool. It is not affiliated with, endorsed by, or sponsored by Anthropic PBC or OpenAI.",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("the section does not say %q", want)
		}
	}
	for _, m := range Marks {
		for _, f := range m.Files {
			if !strings.Contains(s, "`"+f+"`") {
				t.Fatalf("the section does not name %s's file %s", m.Name, f)
			}
		}
		want := "depict " + m.Depicts + ", © " + m.Owner + ". They are not covered by this project's licence, no licence from " +
			m.Licensor + " is claimed, and they will be removed on request."
		if !strings.Contains(s, want) {
			t.Fatalf("the section does not say %q", want)
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
// files they are compared with: the repository's LICENSE and the systray
// copy (core.autocrlf on Windows would make them differ).
func TestLicenceFilesKeepTheirBytesInGit(t *testing.T) {
	b, err := os.ReadFile("../../../.gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"/LICENSE -text", "agent/internal/notices/licenses/** -text", "agent/third_party/systray/** -text"} {
		if !lines[want] {
			t.Fatalf(".gitattributes has no line %q", want)
		}
	}
}

// The binary's notice and the README say the same about the systray copy:
// upstream's files with our changes are Apache-2.0, the files we added
// are MIT (review m5).
func TestSystrayLicenceAgreesWithTheReadme(t *testing.T) {
	var b bytes.Buffer
	Write(&b, nil, "go1.27.4")
	notice := strings.Join(strings.Fields(b.String()), " ")
	for _, want := range []string{"changes to its files included", "files added by AI Usage: MIT",
		"files added there are under AI Usage's MIT licence"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("the notices do not say %q", want)
		}
	}
	r, err := os.ReadFile("../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.Join(strings.Fields(string(r)), " ")
	for _, want := range []string{"Apache License 2.0 (`agent/third_party/systray/LICENSE`), changes to its files included",
		"Only the files added to the copy", "are under the MIT licence"} {
		if !strings.Contains(readme, want) {
			t.Fatalf("the README does not say %q", want)
		}
	}
}
