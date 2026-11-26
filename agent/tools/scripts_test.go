//go:build linux

// Tests of the release shell scripts (run with bash; Linux only).
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var releaseNames = []string{"ai-usage-linux-amd64", "ai-usage-macos-universal", "ai-usage-windows-amd64.exe"}

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
	if err := os.Remove(filepath.Join(dir, "ai-usage-macos-universal")); err != nil {
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
	for _, want := range []string{"DRY RUN: gh release create v1.2.3 --verify-tag --draft", `--title "AI Usage v1.2.3"`,
		"DRY RUN: gh release edit v1.2.3 --draft=false",
		"AI Usage agent v1.2.3.", "Install it from your team's AI Usage server, not from this page"} {
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
	if err := os.WriteFile(filepath.Join(dir, "ai-usage-linux-amd64"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	check("altered file", dir, "v1.2.3")

	dir = publishable(t)
	f, _ := os.OpenFile(filepath.Join(dir, "SHA256SUMS"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(strings.Repeat("0", 64) + "  ai-usage-extra\n")
	f.Close()
	check("fourth name", dir, "v1.2.3")

	dir = publishable(t)
	if err := os.Remove(filepath.Join(dir, "ai-usage-windows-amd64.exe")); err != nil {
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

// Ruling F4 (review N-4): versions of test builds are never released:
// release.yml's tag check and publish-release.sh refuse them.
func TestReleaseVersionRefusesTestBuilds(t *testing.T) {
	for v, ok := range map[string]bool{
		"0.8.3": true, "v0.8.3": true, "1.2.3-rc.1": true, "0.9.0-beta": true, "1.0.0-circle": true,
		"0.0.0-ci": false, "v0.0.0-ci": false, "0.0.0": false, "0.0.0-dev": false, "0.0.0-dryrun": false,
		"0.0.0-local": false, "0.8.3-ci": false, "0.8.3-rc.ci": false, "0.8.3-ci.1": false, "0.8.3-dev": false,
		"0.8.3-relcheck": false, "0.0.0-relcheck": false,
	} {
		cmd := exec.Command("bash", "-c", `. ./version.sh && release_version "$1"`, "x", v)
		err := cmd.Run()
		if (err == nil) != ok {
			t.Errorf("release_version %s: accepted=%v", v, err == nil)
		}
	}
	for _, tag := range []string{"v0.0.0-ci", "v0.8.3-ci", "v0.0.0-dev"} {
		out, code := script(t, "publish-release.sh", []string{"GH_TOKEN=x"}, publishable(t), tag)
		if code == 0 || !strings.Contains(out, "a test build is never published") {
			t.Fatalf("%s: %d %s", tag, code, out)
		}
	}
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `release_version "${tag#v}"`) {
		t.Fatal("release.yml does not refuse a test build's tag")
	}
}

// buildAgent builds the agent at version (and, if not "", with the CI
// build's version set to ci) into a new folder.
func buildAgent(t *testing.T, version, ci string) string {
	t.Helper()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	out := filepath.Join(t.TempDir(), "ai-usage")
	ld := "-X github.com/Deep-In-Sight/ai-usage-client/agent/internal/version.Version=" + version
	if ci != "" {
		ld += " -X github.com/Deep-In-Sight/ai-usage-client/agent/internal/cli.ciVersion=" + ci
	}
	cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags", ld, "-o", out, "../cmd/ai-usage")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, b)
	}
	return out
}

// Ruling F4: the check run on release executables passes a release
// build, and fails one that reports the CI version, one that reports
// another version than the release's, and one that honours the CI
// build's test switches under a release's version.
func TestCheckRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the agent three times")
	}
	rel := buildAgent(t, "1.2.3", "")
	if out, code := script(t, "check-release.sh", nil, rel, "1.2.3"); code != 0 || !strings.Contains(out, "no test switch honoured") {
		t.Fatalf("a release build failed: %d %s", code, out)
	}
	if out, code := script(t, "check-release.sh", nil, rel, "1.2.4"); code == 0 || !strings.Contains(out, "not 1.2.4") {
		t.Fatalf("another version passed: %d %s", code, out)
	}
	ci := buildAgent(t, "0.0.0-ci", "")
	for _, want := range []string{"0.0.0-ci", "1.2.3"} {
		if out, code := script(t, "check-release.sh", nil, ci, want); code == 0 {
			t.Fatalf("the CI build passed as %s: %s", want, out)
		}
	}
	// re-review R-1 / N-b: the version is compared whole, and a rehearsal
	// (ordinary CI) takes only a version that can never be released
	pre := buildAgent(t, "1.2.3-relcheck", "")
	if out, code := script(t, "check-release.sh", nil, pre, "1.2.3"); code == 0 || !strings.Contains(out, "not 1.2.3") {
		t.Fatalf("1.2.3-relcheck passed as 1.2.3: %d %s", code, out)
	}
	if out, code := script(t, "check-release.sh", nil, pre, "1.2.3-relcheck"); code == 0 {
		t.Fatalf("a never-released version passed a real check: %s", out)
	}
	if out, code := script(t, "check-release.sh", nil, "--rehearsal", pre, "1.2.3-relcheck"); code != 0 {
		t.Fatalf("the rehearsal failed: %d %s", code, out)
	}
	if out, code := script(t, "check-release.sh", nil, "--rehearsal", rel, "1.2.3"); code == 0 || !strings.Contains(out, "can never be released") {
		t.Fatalf("a rehearsal took a release version: %d %s", code, out)
	}
	if out, code := script(t, "check-release.sh", nil, "--rehearsal", ci, "0.0.0-ci"); code == 0 || !strings.Contains(out, "behaves as a CI build") {
		t.Fatalf("the CI build passed the rehearsal: %d %s", code, out)
	}
	bad := buildAgent(t, "1.2.3", "1.2.3") // a release's version, a CI build's behaviour
	out, code := script(t, "check-release.sh", nil, bad, "1.2.3")
	if code == 0 || !strings.Contains(out, "behaves as a CI build") || !strings.Contains(out, "AI_USAGE_TEST_CODEX_FALLBACK") {
		t.Fatalf("a build that honours the test switches passed: %d %s", code, out)
	}
}

// Re-review R-1: the release workflow's check is the reusable
// release-check.yml, which ordinary CI also runs on every push (a
// rehearsal on files built by tools/build.sh), and it reads no build
// settings (with -trimpath, Go records no -ldflags).
func TestReleaseCheckRunsInOrdinaryCI(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	rel, ci, chk := read("../../.github/workflows/release.yml"), read("../../.github/workflows/ci.yml"), read("../../.github/workflows/release-check.yml")
	for _, want := range []string{"uses: ./.github/workflows/release-check.yml", "artifact: release-files", "rehearsal: false"} {
		if !strings.Contains(rel, want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
	for _, want := range []string{"uses: ./.github/workflows/release-check.yml", "artifact: relcheck-files", "rehearsal: true", `tools/build.sh "$VERSION"`} {
		if !strings.Contains(ci, want) {
			t.Errorf("ci.yml lacks %q", want)
		}
	}
	for _, want := range []string{"agent/tools/check-release.sh --rehearsal", "agent/tools/check-release.sh dist/ai-usage-linux-amd64", "check-release.ps1", "windows-2025"} {
		if !strings.Contains(chk, want) {
			t.Errorf("release-check.yml lacks %q", want)
		}
	}
	for name, s := range map[string]string{"release.yml": rel, "release-check.yml": chk} {
		if strings.Contains(s, "go version -m") {
			t.Errorf("%s reads build settings, which -trimpath leaves without -ldflags", name)
		}
	}
}

// The CI test steps name every failing test in one annotation (a step
// keeps only ten, and the first failure lines can all be one package's):
// ci/failing-tests.sh reads a go test log, plain or -v.
func TestFailingTestsListsEveryPackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash")
	}
	log := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(log, []byte(`=== RUN   TestA
--- FAIL: TestA (0.00s)
    a_test.go:10: bad
    --- FAIL: TestA/sub (0.00s)
--- FAIL: TestB (0.01s)
FAIL
FAIL	example.com/x/pkga	0.123s
ok  	example.com/x/pkgb	0.1s
# example.com/x/pkgc
pkgc/c.go:3:1: syntax error
FAIL	example.com/x/pkgc [build failed]
--- FAIL: TestC (0.00s)
panic: boom
FAIL	example.com/x/pkgd	0.2s
FAIL
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "../ci/failing-tests.sh", log).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "example.com/x/pkga: TestA TestB; example.com/x/pkgc [build failed]: (no test named); example.com/x/pkgd: TestC" {
		t.Fatal(got)
	}
}
