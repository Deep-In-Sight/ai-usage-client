package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
)

// CLI is a codex executable and its version.
type CLI struct{ Path, Version string }

// defaultBrewDirs are Homebrew's folders on macOS; brewDirs is a var so
// that tests can point it elsewhere.
var (
	defaultBrewDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	brewDirs        = defaultBrewDirs
)

// FindCLI lists the codex executables to consider, in the order of design
// D15: the path recorded at install (codex_path), the official standalone
// installer's place in the Codex folder, PATH (npm shims resolved to the
// binary they run; the agent never runs node, cmd or PowerShell), Homebrew
// and the desktop apps (macOS), and the binaries the VS Code extension
// ships (newest first). Each once; only files that exist. getenv is the
// environment to search PATH in (install: the user's shell).
func FindCLI(cfg config.Config, userHome string, getenv func(string) string) []string {
	return findCLI(runtime.GOOS, cfg, userHome, getenv)
}

func findCLI(goos string, cfg config.Config, userHome string, getenv func(string) string) []string {
	name := "codex"
	if goos == "windows" {
		name = "codex.exe"
	}
	var out []string
	add := func(p string) {
		if p == "" || !isExecutable(goos, p) {
			return
		}
		p = filepath.Clean(p)
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if filepath.IsAbs(cfg.CodexPath) {
		add(cfg.CodexPath) // a relative one would depend on the agent's working directory (review m-4)
	}
	add(filepath.Join(Home(cfg, userHome), "packages", "standalone", "current", "bin", name))
	if p := LookPath(goos, getenv); p != "" {
		add(p)
	}
	if goos == "darwin" {
		for _, d := range brewDirs {
			add(filepath.Join(d, "codex"))
		}
		// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/PathEnvironment.swift
		// codexWellKnownPaths @ a26ca57: the codex the ChatGPT and Codex desktop apps ship.
		for _, root := range []string{filepath.Join(userHome, "Applications"), "/Applications"} {
			add(filepath.Join(root, "ChatGPT.app", "Contents", "Resources", "codex"))
			add(filepath.Join(root, "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin", "codex"))
			add(filepath.Join(root, "Codex.app", "Contents", "Resources", "codex"))
		}
	}
	for _, p := range extensionCLIs(userHome, name) {
		add(p)
	}
	return out
}

// extensionCLIs are the codex binaries of the OpenAI VS Code extension
// (any VS Code flavour's folder), the newest extension folder first.
func extensionCLIs(userHome, name string) []string {
	matches, _ := filepath.Glob(filepath.Join(userHome, ".vscode*", "extensions", "openai.chatgpt-*", "bin", "*", name))
	type found struct {
		path string
		mod  int64
	}
	var fs []found
	for _, m := range matches {
		ext := filepath.Dir(filepath.Dir(filepath.Dir(m)))
		st, err := os.Stat(ext)
		if err != nil {
			continue
		}
		fs = append(fs, found{m, st.ModTime().UnixNano()})
	}
	slices.SortStableFunc(fs, func(a, b found) int {
		switch {
		case a.mod > b.mod:
			return -1
		case a.mod < b.mod:
			return 1
		}
		return strings.Compare(b.path, a.path)
	})
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.path)
	}
	return out
}

// LookPath finds codex on PATH as getenv gives it, as the shell would
// (absolute PATH entries only): an npm shim is resolved to the native binary it runs, any other link to
// its target. "" when there is none (or only a shim whose binary is
// missing).
func LookPath(goos string, getenv func(string) string) string {
	path := getenv("PATH")
	if goos == "windows" && path == "" {
		path = getenv("Path")
	}
	var names []string
	if goos == "windows" {
		exts := strings.Split(strings.ToLower(getenv("PATHEXT")), ";")
		if len(exts) == 1 && exts[0] == "" {
			exts = []string{".com", ".exe", ".bat", ".cmd"}
		}
		for _, e := range exts {
			if e != "" {
				names = append(names, "codex"+e)
			}
		}
		names = append(names, "codex.ps1") // PowerShell's shim, after cmd's
	} else {
		names = []string{"codex"}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			// a relative entry (./node_modules/.bin) names a folder of
			// whatever directory the shell is in, never searched (as
			// exec.LookPath refuses it since Go 1.19; review m-4)
			continue
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			st, err := os.Stat(p)
			if err != nil || st.IsDir() {
				continue
			}
			if native := ResolveShim(p); native != "" {
				return native
			}
			if isShim(p) {
				continue // a shim without its binary: never run through node, cmd or PowerShell
			}
			if !isExecutable(goos, p) {
				continue
			}
			if real, err := filepath.EvalSymlinks(p); err == nil {
				return real
			}
			return p
		}
	}
	return ""
}

// npmTarget is the npm platform name and the Rust target of this OS and
// architecture, as the @openai/codex npm package names them (its launcher
// codex-cli/bin/codex.js in openai/codex).
func npmTarget(goos, goarch string) (plat, triple string) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	cpu := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	switch goos {
	case "linux":
		return "linux-" + arch, cpu + "-unknown-linux-musl"
	case "darwin":
		return "darwin-" + arch, cpu + "-apple-darwin"
	case "windows":
		return "win32-" + arch, cpu + "-pc-windows-msvc"
	}
	return "", ""
}

// isShim: a script that starts codex through another program (npm's
// codex.js, codex.cmd, codex.ps1 or a "#!" script).
func isShim(p string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		real = p
	}
	switch strings.ToLower(filepath.Ext(real)) {
	case ".js", ".cjs", ".mjs", ".cmd", ".bat", ".ps1":
		return true
	}
	f, err := os.Open(real)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 2)
	n, _ := f.Read(head)
	return n == 2 && bytes.Equal(head, []byte("#!"))
}

// ResolveShim gives the native codex binary an npm shim at path runs, ""
// when path is no shim or the binary is missing.
//
// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/PathEnvironment.swift
// npmNativeExecutable @ a26ca57: the package's native payload is
// vendor/<target>/bin/codex, or vendor/<target>/codex/codex in older
// releases; the launcher itself is never run. Newer releases put the
// payload in a platform package next to @openai/codex
// (@openai/codex-<platform>), which is looked at first.
func ResolveShim(path string) string {
	if !isShim(path) {
		return ""
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	var pkg string
	if strings.EqualFold(filepath.Base(real), "codex.js") {
		pkg = filepath.Dir(filepath.Dir(real)) // <pkg>/bin/codex.js
	} else {
		// npm's codex.cmd / codex.ps1 / sh shim next to node_modules
		dir := filepath.Dir(real)
		for _, mods := range []string{filepath.Join(dir, "node_modules"), filepath.Join(filepath.Dir(dir), "lib", "node_modules")} {
			if st, err := os.Stat(filepath.Join(mods, "@openai", "codex")); err == nil && st.IsDir() {
				pkg = filepath.Join(mods, "@openai", "codex")
				break
			}
		}
		if pkg == "" {
			return ""
		}
	}
	plat, triple := npmTarget(runtime.GOOS, runtime.GOARCH)
	if triple == "" {
		return ""
	}
	name := "codex"
	if runtime.GOOS == "windows" {
		name = "codex.exe"
	}
	for _, p := range []string{
		filepath.Join(filepath.Dir(pkg), "codex-"+plat, "vendor", triple, "bin", name),
		filepath.Join(pkg, "node_modules", "@openai", "codex-"+plat, "vendor", triple, "bin", name),
		filepath.Join(pkg, "vendor", triple, "bin", name),
		filepath.Join(pkg, "vendor", triple, "codex", name),
	} {
		if isExecutable(runtime.GOOS, p) {
			return p
		}
	}
	return ""
}

// isExecutable: a regular file this user may run (Windows: an .exe).
func isExecutable(goos, p string) bool {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	if goos == "windows" {
		return strings.EqualFold(filepath.Ext(p), ".exe")
	}
	return st.Mode().Perm()&0o111 != 0
}

var versionPattern = regexp.MustCompile(`(?m)^codex-cli (\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?\s*$`)

// ParseVersion reads "codex-cli X.Y.Z" from `codex --version`.
func ParseVersion(out string) (string, bool) {
	m := versionPattern.FindStringSubmatch(out)
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2] + "." + m[3] + m[4], true
}

// compareVersions orders X.Y.Z[-pre] versions; a pre-release is lower than
// its release.
func compareVersions(a, b string) int {
	pa, preA, _ := strings.Cut(a, "-")
	pb, preB, _ := strings.Cut(b, "-")
	na, nb := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(na[i])
		y, _ := strconv.Atoi(nb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	return strings.Compare(preA, preB)
}

// PickCLI is the candidate with the highest version (design D15: Codex
// versions that alternate rewrite Codex's own files each time); the first
// of equal ones. version runs `<path> --version` (Task 9: with a time
// limit, only at start and when the file changed).
func PickCLI(paths []string, version func(path string) (string, error)) (CLI, bool) {
	var best CLI
	for _, p := range paths {
		out, err := version(p)
		if err != nil {
			continue
		}
		v, ok := ParseVersion(out)
		if !ok {
			continue
		}
		if best.Path == "" || compareVersions(v, best.Version) > 0 {
			best = CLI{Path: p, Version: v}
		}
	}
	return best, best.Path != ""
}
