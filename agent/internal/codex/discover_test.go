package codex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sharedread"
)

// exe writes an executable file (a stand-in; it is never run).
func exe(t *testing.T, path string) string {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte("\x7fELF fake"), 0o755))
	return path
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "codex.exe"
	}
	return "codex"
}

// npmPackage lays out an npm install of @openai/codex under prefix as npm
// does on this OS and returns the shim on PATH and the native binary.
func npmPackage(t *testing.T, prefix string, layout string) (shim, native string) {
	t.Helper()
	plat, triple := npmTarget(runtime.GOOS, runtime.GOARCH)
	var modules string
	if runtime.GOOS == "windows" {
		modules = filepath.Join(prefix, "node_modules")
	} else {
		modules = filepath.Join(prefix, "lib", "node_modules")
	}
	pkg := filepath.Join(modules, "@openai", "codex")
	writeFile(t, filepath.Join(pkg, "bin", "codex.js"), "#!/usr/bin/env node\n")
	switch layout {
	case "platform-package":
		native = exe(t, filepath.Join(modules, "@openai", "codex-"+plat, "vendor", triple, "bin", exeName()))
	case "vendor-bin":
		native = exe(t, filepath.Join(pkg, "vendor", triple, "bin", exeName()))
	case "legacy":
		native = exe(t, filepath.Join(pkg, "vendor", triple, "codex", exeName()))
	case "broken":
	}
	if runtime.GOOS == "windows" {
		shim = filepath.Join(prefix, "codex.cmd")
		writeFile(t, shim, "@ECHO off\r\nnode \"%~dp0\\node_modules\\@openai\\codex\\bin\\codex.js\" %*\r\n")
		writeFile(t, filepath.Join(prefix, "codex.ps1"), "#!/usr/bin/env pwsh\n")
		return shim, native
	}
	shim = filepath.Join(prefix, "bin", "codex")
	must(t, os.MkdirAll(filepath.Dir(shim), 0o700))
	must(t, os.Symlink(filepath.Join("..", "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), shim))
	return shim, native
}

// An npm shim is resolved to the native binary the package ships (D15:
// the agent never runs node, cmd or PowerShell).
func TestResolveShim(t *testing.T) {
	for _, layout := range []string{"platform-package", "vendor-bin", "legacy"} {
		t.Run(layout, func(t *testing.T) {
			shim, native := npmPackage(t, t.TempDir(), layout)
			got := ResolveShim(shim)
			want, _ := filepath.EvalSymlinks(native)
			if g, _ := filepath.EvalSymlinks(got); g != want {
				t.Fatalf("got %q, want %q", got, native)
			}
		})
	}
	shim, _ := npmPackage(t, t.TempDir(), "broken")
	eq(t, ResolveShim(shim), "")
	// a native binary is no shim
	eq(t, ResolveShim(exe(t, filepath.Join(t.TempDir(), exeName()))), "")
	eq(t, ResolveShim(filepath.Join(t.TempDir(), "missing")), "")
}

// Candidates in the order of design D15, each once, only files that exist.
func TestFindCLIOrder(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	recorded := exe(t, filepath.Join(t.TempDir(), "recorded", exeName()))
	standalone := exe(t, filepath.Join(codexHome, "packages", "standalone", "current", "bin", exeName()))
	npmPrefix := t.TempDir()
	shim, native := npmPackage(t, npmPrefix, "vendor-bin")
	brew := t.TempDir()
	brewed := exe(t, filepath.Join(brew, "codex"))
	extOld := exe(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-26.1001.0-linux-x64", "bin", "linux-x86_64", exeName()))
	extNew := exe(t, filepath.Join(home, ".vscode-insiders", "extensions", "openai.chatgpt-26.1120.0-linux-x64", "bin", "linux-x86_64", exeName()))
	old := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(filepath.Dir(filepath.Dir(filepath.Dir(extOld))), old, old))
	brewDirs = []string{brew, filepath.Join(t.TempDir(), "none")}
	t.Cleanup(func() { brewDirs = defaultBrewDirs })

	pathDirs := filepath.Dir(shim) + string(os.PathListSeparator) + filepath.Join(t.TempDir(), "empty")
	getenv := func(k string) string {
		switch k {
		case "PATH", "Path":
			return pathDirs
		case "PATHEXT":
			return ".COM;.EXE;.BAT;.CMD"
		}
		return ""
	}
	cfg := config.Config{CodexPath: recorded, CodexHome: codexHome}
	got := FindCLI(cfg, home, getenv)
	nativeReal, _ := filepath.EvalSymlinks(native)
	want := []string{recorded, standalone, nativeReal}
	if runtime.GOOS == "darwin" {
		want = append(want, brewed)
	}
	want = append(want, extNew, extOld)
	eq(t, got, want)

	// a recorded path that is also found on PATH is listed once, first
	cfg.CodexPath = nativeReal
	got = FindCLI(cfg, home, getenv)
	eq(t, got[:2], []string{nativeReal, standalone})
	eq(t, len(got), len(want)-1)
	// a recorded path that is gone is skipped
	cfg.CodexPath = filepath.Join(t.TempDir(), "gone", exeName())
	got = FindCLI(cfg, home, getenv)
	eq(t, got[0], standalone)
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"codex-cli 0.160.1\n":          "0.160.1",
		"codex-cli 0.172.0-alpha.3":    "0.172.0-alpha.3",
		"warning: x\ncodex-cli 1.2.3 ": "1.2.3",
	} {
		v, ok := ParseVersion(in)
		if !ok || v != want {
			t.Fatalf("%q: %q %v", in, v, ok)
		}
	}
	for _, in := range []string{"", "codex 0.1.0", "codex-cli x.y.z", "codex-cli 1.2"} {
		if v, ok := ParseVersion(in); ok {
			t.Fatalf("%q gave %q", in, v)
		}
	}
}

// With several, the highest version is used (design D15: alternating
// versions rewrite Codex's system skills each time).
func TestPickCLIHighest(t *testing.T) {
	versions := map[string]string{"/a/codex": "codex-cli 0.160.1", "/b/codex": "codex-cli 0.172.0", "/c/codex": "codex-cli 0.99.9",
		"/d/codex": "garbage", "/e/codex": "codex-cli 0.172.0-alpha.1"}
	run := func(p string) (string, error) {
		if p == "/f/codex" {
			return "", errors.New("exec failed")
		}
		return versions[p], nil
	}
	c, ok := PickCLI([]string{"/a/codex", "/d/codex", "/f/codex", "/c/codex", "/b/codex", "/e/codex"}, run)
	eq(t, ok, true)
	eq(t, c, CLI{Path: "/b/codex", Version: "0.172.0"})
	// the first of equal versions (the earlier candidate) stays
	versions["/g/codex"] = "codex-cli 0.172.0"
	c, _ = PickCLI([]string{"/b/codex", "/g/codex"}, run)
	eq(t, c.Path, "/b/codex")
	if _, ok := PickCLI([]string{"/d/codex", "/f/codex"}, run); ok {
		t.Fatal("picked one without a version")
	}
}

// Review m-4: a relative PATH entry (./node_modules/.bin) names a folder
// of whatever directory a shell happened to be in; it is never searched,
// and a recorded codex_path that is not absolute is never a candidate.
func TestRelativePathsAreNeverCandidates(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	exe(t, filepath.Join(dir, "rel", exeName()))
	abs := t.TempDir()
	absCLI := exe(t, filepath.Join(abs, exeName()))
	for _, c := range []struct{ path, want string }{
		{"rel", ""},
		{"." + string(filepath.Separator) + "rel", ""},
		{"rel" + string(os.PathListSeparator) + abs, absCLI},
	} {
		getenv := func(k string) string {
			switch k {
			case "PATH", "Path":
				return c.path
			case "PATHEXT":
				return ".EXE"
			}
			return ""
		}
		got := LookPath(runtime.GOOS, getenv)
		if got != "" {
			got, _ = filepath.EvalSymlinks(got)
		}
		want := c.want
		if want != "" {
			want, _ = filepath.EvalSymlinks(want)
		}
		eq(t, got, want)
	}
	cfg := config.Config{CodexPath: filepath.Join("rel", exeName()), CodexHome: filepath.Join(t.TempDir(), "none")}
	eq(t, len(FindCLI(cfg, t.TempDir(), func(string) string { return "" })), 0)
}

// Review N-2: telling a shim from a binary reads the file like every
// other read of Codex's files, bounded in time: while an earlier read of
// that file still hangs, the look ends at once (the file counts as no
// shim) instead of waiting.
func TestShimCheckIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "codex")
	must(t, os.WriteFile(p, []byte("#!/bin/sh\nexec node codex.js\n"), 0o755))
	eq(t, isShim(p), true)
	release, holding := make(chan struct{}), make(chan struct{})
	go func() {
		_ = sharedread.Do(p, func(*os.File) error { close(holding); <-release; return nil })
	}()
	<-holding
	defer close(release)
	start := time.Now()
	eq(t, isShim(p), false)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v", d)
	}
}
