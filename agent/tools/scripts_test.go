//go:build linux

// Tests of the release shell scripts (run with bash; Linux only).
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var releaseNames = []string{"claude-usage-linux-amd64", "claude-usage-macos-universal", "claude-usage-windows-amd64.exe"}

// fakeDist writes the three release files (different contents) to a new
// folder.
func fakeDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for i, n := range releaseNames {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(strings.Repeat("x", i+1)+n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// script runs a script from this folder with a clean environment plus env.
func script(t *testing.T, name string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{name}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func sumLine(t *testing.T, dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]) + "  " + name + "\n"
}

func TestSumsMatchesSha256sum(t *testing.T) {
	dir := fakeDist(t)
	if out, code := script(t, "sums.sh", nil, dir); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, n := range releaseNames {
		want += sumLine(t, dir, n)
	}
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	out, err := exec.Command("sha256sum", releaseNames...).Output()
	if err == nil {
		cmd := exec.Command("sha256sum", releaseNames...)
		cmd.Dir = dir
		out, _ = cmd.Output()
		if string(out) != want {
			t.Fatalf("sha256sum says\n%s", out)
		}
	}
}

func TestSumsRequireAll(t *testing.T) {
	dir := fakeDist(t)
	if err := os.Remove(filepath.Join(dir, "claude-usage-macos-universal")); err != nil {
		t.Fatal(err)
	}
	if _, code := script(t, "sums.sh", nil, dir, "--require-all"); code == 0 {
		t.Fatal("--require-all passed without the macOS file")
	}
	if out, code := script(t, "sums.sh", nil, dir); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Fatalf("%d lines", n)
	}
}

func publishable(t *testing.T) string {
	t.Helper()
	dir := fakeDist(t)
	if out, code := script(t, "sums.sh", nil, dir, "--require-all"); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	return dir
}

func TestPublishDryRun(t *testing.T) {
	dir := publishable(t)
	out, code := script(t, "publish-release.sh", []string{"DRY_RUN=1"}, dir, "v1.2.3")
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	for _, want := range []string{"DRY RUN: gh release create v1.2.3 --verify-tag --draft", `--title "Claude Usage v1.2.3"`,
		"DRY RUN: gh release edit v1.2.3 --draft=false",
		"Claude Usage agent v1.2.3.", "Install it from your team's Claude Usage server, not from this page"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q not in\n%s", want, out)
		}
	}
	for _, n := range append(releaseNames, "SHA256SUMS") {
		if !strings.Contains(out, filepath.Join(dir, n)) {
			t.Fatalf("%s not in\n%s", n, out)
		}
	}
	if strings.Contains(out, "--prerelease") {
		t.Fatal("a release is not a pre-release")
	}
	if strings.Contains(out, "::notice") {
		t.Fatal("annotation outside Actions")
	}
	out, _ = script(t, "publish-release.sh", []string{"DRY_RUN=1", "GITHUB_ACTIONS=true"}, dir, "v1.2.3")
	if !strings.Contains(out, "::notice title=release dry run::gh release create v1.2.3 --verify-tag --draft") {
		t.Fatalf("no annotation:\n%s", out)
	}
}

func TestPublishDryRunPrerelease(t *testing.T) {
	dir := publishable(t)
	out, code := script(t, "publish-release.sh", []string{"DRY_RUN=1"}, dir, "v1.2.3-rc.1")
	if code != 0 || !strings.Contains(out, "--prerelease") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestPublishRefuses(t *testing.T) {
	check := func(name, dir, tag string, env ...string) {
		t.Helper()
		out, code := script(t, "publish-release.sh", append([]string{"DRY_RUN=1"}, env...), dir, tag)
		if code == 0 || strings.Contains(out, "gh release create") {
			t.Fatalf("%s: accepted (%d)\n%s", name, code, out)
		}
	}
	check("tag without v", publishable(t), "1.2.3")
	check("odd tag", publishable(t), "v1.2")

	dir := publishable(t)
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("extra file", dir, "v1.2.3")

	dir = publishable(t)
	if err := os.WriteFile(filepath.Join(dir, "claude-usage-linux-amd64"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	check("altered file", dir, "v1.2.3")

	dir = publishable(t)
	f, _ := os.OpenFile(filepath.Join(dir, "SHA256SUMS"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(strings.Repeat("0", 64) + "  claude-usage-extra\n")
	f.Close()
	check("fourth name", dir, "v1.2.3")

	dir = publishable(t)
	if err := os.Remove(filepath.Join(dir, "claude-usage-windows-amd64.exe")); err != nil {
		t.Fatal(err)
	}
	check("missing file", dir, "v1.2.3")

	// a real publish needs a token
	out, code := script(t, "publish-release.sh", nil, publishable(t), "v1.2.3")
	if code == 0 || !strings.Contains(out, "GH_TOKEN") {
		t.Fatalf("published without a token? %d %s", code, out)
	}
}

func TestBuildRejectsBadVersion(t *testing.T) {
	for _, v := range []string{"1.2", "v1.2.3", "1.2.3-", "1.2.3+build", "1.2.3 x"} {
		out, code := script(t, "build.sh", nil, v)
		if code != 2 {
			t.Fatalf("%q: %d %s", v, code, out)
		}
	}
	if _, code := script(t, "build-macos.sh", nil, "1.2"); code != 2 {
		t.Fatalf("build-macos.sh accepted 1.2: %d", code)
	}
}
