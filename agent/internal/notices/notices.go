// Package notices holds the licence texts of the third-party code compiled
// into the agent, so they reach everyone who gets the binary ("ai-usage
// licenses"). The files under licenses/ are the modules' own, unchanged
// (TestNoticesAreTheModulesFiles), and every compiled-in module has an
// entry (TestEveryCompiledModuleHasANotice).
package notices

import (
	"embed"
	"fmt"
	"io"
	"path"
	"runtime/debug"
	"strings"
)

//go:embed licenses
var files embed.FS

// GoPath stands for the Go standard library and runtime.
const GoPath = "Go"

// Module is one piece of third-party code and its licence files.
type Module struct {
	Path    string
	Licence string
	Note    string
	Files   []string // in the module's root, embedded under licenses/<Path>/
}

func (m Module) embedded(f string) string {
	dir := m.Path
	if m.Path == GoPath {
		dir = "go"
	}
	return path.Join("licenses", dir, f)
}

// Modules is everything third-party in the release binaries (some only on
// some OSes: godbus and x/net are Linux/Windows only, systray is in the tray
// builds).
var Modules = []Module{
	{Path: GoPath, Licence: "BSD 3-Clause", Note: "the Go standard library and runtime", Files: []string{"LICENSE", "PATENTS"}},
	{Path: "fyne.io/systray", Licence: "Apache License 2.0",
		Note:  "modified: the copy in agent/third_party/systray of the source repository; PATCHED.md there lists the changes",
		Files: []string{"LICENSE"}},
	{Path: "github.com/godbus/dbus/v5", Licence: "BSD 2-Clause", Files: []string{"LICENSE"}},
	{Path: "github.com/miekg/dns", Licence: "BSD 3-Clause", Files: []string{"LICENSE"}},
	{Path: "golang.org/x/net", Licence: "BSD 3-Clause", Files: []string{"LICENSE", "PATENTS"}},
	{Path: "golang.org/x/sys", Licence: "BSD 3-Clause", Files: []string{"LICENSE", "PATENTS"}},
}

// BuildVersions are the versions of the modules in this binary.
func BuildVersions() map[string]string {
	v := map[string]string{}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			v[d.Path] = d.Version // a replaced module keeps the version it stands for
		}
	}
	return v
}

const rule = "----------------------------------------------------------------------"

// Write prints the notices: each module with its version when this binary
// has it (versions; goVersion for Go), its licence and the licence files.
func Write(w io.Writer, versions map[string]string, goVersion string) {
	fmt.Fprintln(w, "AI Usage contains the third-party software below, used under the licences")
	fmt.Fprintln(w, "shown. The full texts follow each entry.")
	fmt.Fprintln(w, "The Clawd mascot artwork is (c) Anthropic PBC.")
	for _, m := range Modules {
		fmt.Fprintf(w, "\n%s\n", rule)
		v := versions[m.Path]
		if m.Path == GoPath {
			v = goVersion
		}
		fmt.Fprintln(w, strings.TrimSpace(m.Path+" "+v))
		if m.Note != "" {
			fmt.Fprintf(w, "(%s)\n", m.Note)
		}
		fmt.Fprintf(w, "Licence: %s\n", m.Licence)
		for _, f := range m.Files {
			b, err := files.ReadFile(m.embedded(f))
			if err != nil { // cannot happen: TestNoticesAreTheModulesFiles
				continue
			}
			fmt.Fprintf(w, "\n%s:\n\n%s", f, b)
			if len(b) > 0 && b[len(b)-1] != '\n' {
				fmt.Fprintln(w)
			}
		}
	}
}
