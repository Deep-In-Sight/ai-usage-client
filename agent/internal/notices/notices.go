// Package notices holds AI Usage's own licence and the licence texts of the
// third-party code compiled into the agent and of the projects whose logic
// it ports (borrowed/), so they reach everyone who gets the binary
// ("ai-usage licenses"). licenses/ai-usage/LICENSE is the
// repository's LICENSE (TestOwnLicenceIsTheRepositorysLicense); the other
// files under licenses/ are the modules' own, unchanged
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

//go:embed licenses borrowed
var files embed.FS

// OwnLicence is AI Usage's licence, a copy of the repository's LICENSE.
const OwnLicence = "licenses/ai-usage/LICENSE"

// Mark is another company's artwork or logo that the program shows,
// used without a licence from its owner.
type Mark struct {
	Name     string   // as the notices name it: "The Clawd artwork"
	Shown    string   // where the program shows it
	Depicts  string   // what it is
	Owner    string   // the rights holder: "Anthropic PBC"
	Licensor string   // the owner in "no licence from … is claimed"
	Files    []string // the repository's files with it (the README names them)
}

// Marks are the other companies' marks in the program. Adding one is an
// entry here and a bullet in the README's "Third-party marks" section
// (TestReadmeListsEveryMark); the notices then state the same three
// things for it: not covered by the MIT licence, no licence claimed,
// removed on request.
var Marks = []Mark{
	{Name: "The Clawd artwork", Shown: "which the tray icon shows while Claude Code is signed in",
		Depicts: "the Claude Code mascot", Owner: "Anthropic PBC", Licensor: "Anthropic",
		Files: []string{"claude-usage@localhost/clawd.svg", "agent/internal/icon/mascot.go",
			"gnome-extension/ai-usage-panel@localhost/clawd.svg",
			"agent/internal/gnomeext/gnome42/ai-usage-panel@localhost/clawd.svg",
			"agent/internal/gnomeext/gnome46/ai-usage-panel@localhost/clawd.svg"}},
}

// Borrowed is a public project whose logic the agent ports (rewritten in
// Go, not copied). Each ported piece has a source comment
// "Ported from <Project> (<Licence>) <file> @ <Commit>: <what>."
// (TestEveryPortedFromCommentHasALicence), and the project's licence is
// embedded at File, with a SOURCE file (URL, commit, date retrieved) next to
// it. The README's Credits section names it too
// (TestReadmeCreditsListEveryBorrowedProject).
type Borrowed struct {
	Project string   // "sirmalloc/ccstatusline"
	URL     string   // "https://github.com/sirmalloc/ccstatusline"
	Commit  string   // the commit the logic was ported from
	Licence string   // "MIT"
	What    []string // one line per ported piece
	File    string   // "borrowed/ccstatusline/LICENSE"
}

// BorrowedCode lists every project the agent ports logic from.
var BorrowedCode = []Borrowed{
	{Project: "sirmalloc/ccstatusline", URL: "https://github.com/sirmalloc/ccstatusline", Commit: "0c94df7", Licence: "MIT",
		What: []string{"reading Claude usage replies: the 5-hour \"session\" entry, placeholder entries, " +
			"model names matched in part, each value falling back to the older reply shape on its own " +
			"(agent/internal/usage)"},
		File: "borrowed/ccstatusline/LICENSE"},
	{Project: "steipete/CodexBar", URL: "https://github.com/steipete/CodexBar", Commit: "a26ca57", Licence: "MIT",
		What: []string{"reading the Codex sign-in without writing it: a missing or partly written auth.json read again " +
			"twice, 50 ms apart, and when it needs renewing (the access token's expiry with a 5-minute window, else " +
			"last_refresh plus 8 days) (agent/internal/codex)",
			"reading chatgpt_base_url from Codex's config.toml line by line (agent/internal/codex)",
			"finding the codex executable on PATH, resolving an npm install's launcher to the native binary it ships, " +
				"which install only records (agent/internal/codex)",
			"the Codex usage request: the address made from chatgpt_base_url (wham/usage or api/codex/usage), its headers " +
				"with the product's own User-Agent and its 30-second limit (agent/internal/codex)",
			"naming Codex limit windows by their length, never by their position in the reply (agent/internal/codex)",
			"asking the Codex CLI's app-server for the rate limits (initialize, initialized, account/rateLimits/read; " +
				"replies matched by id) and ending it by closing its input, then stopping its process tree, in code that " +
				"only test builds use: a released agent never starts the Codex CLI (agent/internal/codex)"},
		File: "borrowed/CodexBar/LICENSE"},
}

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
		Note: "modified: the copy in agent/third_party/systray of the source repository; PATCHED.md there lists the changes; " +
			"files added there are under AI Usage's MIT licence",
		Files: []string{"LICENSE"}},
	{Path: "github.com/godbus/dbus/v5", Licence: "BSD 2-Clause", Files: []string{"LICENSE"}},
	{Path: "github.com/miekg/dns", Licence: "BSD 3-Clause", Files: []string{"AUTHORS", "COPYRIGHT", "LICENSE"}},
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

// wrap prints text as lines of at most 76 columns, the first starting
// with lead and the others indented to match.
func wrap(w io.Writer, lead, text string) {
	line, words := lead, 0
	for _, word := range strings.Fields(text) {
		if words > 0 && len(line)+1+len(word) > 76 {
			fmt.Fprintln(w, line)
			line, words = strings.Repeat(" ", len(lead)), 0
		}
		if words > 0 {
			line += " "
		}
		line += word
		words++
	}
	fmt.Fprintln(w, line)
}

const rule = "----------------------------------------------------------------------"

// Independence is the notice of design D13, the same as the README's.
const Independence = `AI Usage is an independent internal tool. It is not affiliated with, endorsed by, ` +
	`or sponsored by Anthropic PBC or OpenAI. "Claude" and "Claude Code" are trademarks of Anthropic PBC; ` +
	`"OpenAI", "ChatGPT" and "Codex" are trademarks of OpenAI. These names are used only to identify the ` +
	`services whose usage this tool displays.`

// Write prints the notices: AI Usage's own licence and what it does not
// cover, then each module with its version when this binary has it
// (versions; goVersion for Go), its licence and the licence files.
func Write(w io.Writer, versions map[string]string, goVersion string) {
	fmt.Fprintln(w, "AI Usage")
	fmt.Fprintln(w, "Licence: MIT License")
	wrap(w, "", Independence)
	fmt.Fprintln(w, "These parts of the program are not covered by this licence:")
	for _, m := range Marks {
		wrap(w, "- ", fmt.Sprintf("%s, %s, depicts %s, (c) %s. It is not covered by this licence, "+
			"no licence from %s is claimed, and it will be removed on request.",
			m.Name, m.Shown, m.Depicts, m.Owner, m.Licensor))
	}
	wrap(w, "- ", "The third-party software listed below keeps its own licence "+
		"(fyne.io/systray: Apache License 2.0, changes to its files included; files added by AI Usage: MIT).")
	if b, err := files.ReadFile(OwnLicence); err == nil { // always: TestOwnLicenceIsTheRepositorysLicense
		fmt.Fprintf(w, "\nLICENSE:\n\n%s", b)
	}
	fmt.Fprintf(w, "\n%s\n\n", rule)
	fmt.Fprintln(w, "AI Usage contains the third-party software below, used under the licences")
	fmt.Fprintln(w, "shown. The full texts follow each entry.")
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
	if len(BorrowedCode) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n\n", rule)
	fmt.Fprintln(w, "AI Usage also contains logic ported from the projects below (rewritten in")
	fmt.Fprintln(w, "Go, not copied), used under the licences shown. The full texts follow each")
	fmt.Fprintln(w, "entry.")
	for _, b := range BorrowedCode {
		fmt.Fprintf(w, "\n%s\n", rule)
		fmt.Fprintf(w, "%s @ %s\n%s\nLicence: %s\n", b.Project, b.Commit, b.URL, b.Licence)
		for _, what := range b.What {
			wrap(w, "- ", what)
		}
		if lic, err := files.ReadFile(b.File); err == nil { // always: TestEveryPortedFromCommentHasALicence
			fmt.Fprintf(w, "\nLICENSE:\n\n%s", lic)
			if len(lic) > 0 && lic[len(lic)-1] != '\n' {
				fmt.Fprintln(w)
			}
		}
	}
}
