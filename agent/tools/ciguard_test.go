package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Re-review R-2: the CI check scripts (they overwrite the Claude and Codex
// sign-ins, install the agent, edit the hosts file and trust a CA) refuse
// to run anywhere but on a GitHub Actions runner: without GITHUB_ACTIONS
// and RUNNER_TEMP they exit non-zero at once and change nothing (the home
// folder and the working folder stay empty; on Windows the hosts file
// keeps its bytes).
func TestCIScriptsRefuseOutsideARunner(t *testing.T) {
	type run struct {
		name string
		cmd  []string
	}
	ciDir, err := filepath.Abs("../ci")
	if err != nil {
		t.Fatal(err)
	}
	var runs []run
	if runtime.GOOS == "windows" {
		pwsh, err := exec.LookPath("pwsh")
		if err != nil {
			t.Skip("no pwsh")
		}
		runs = append(runs, run{"check-windows.ps1", []string{pwsh, "-NoProfile", "-NonInteractive", "-File", filepath.Join(ciDir, "check-windows.ps1")}})
	} else {
		for _, s := range []string{"check-linux.sh", "check-macos.sh"} {
			runs = append(runs, run{s, []string{"bash", filepath.Join(ciDir, s)}})
		}
	}
	hostsFile := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	hostsBefore, _ := os.ReadFile(hostsFile)
	for _, r := range runs {
		for _, partial := range [][]string{nil, {"GITHUB_ACTIONS=true"}, {"RUNNER_TEMP=" + t.TempDir()}} {
			home, wd, local := t.TempDir(), t.TempDir(), t.TempDir()
			// LOCALAPPDATA apart from home: pwsh keeps its own caches there;
			// only the agent's folder in it is checked
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home,
				"LOCALAPPDATA=" + local, "APPDATA=" + local, "CI_SCRIPTS=" + ciDir}
			for _, k := range []string{"SystemRoot", "windir", "TEMP", "TMP", "ProgramFiles", "PSModulePath"} {
				if v, ok := os.LookupEnv(k); ok && runtime.GOOS == "windows" {
					env = append(env, k+"="+v)
				}
			}
			env = append(env, partial...)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cmd := exec.CommandContext(ctx, r.cmd[0], r.cmd[1:]...)
			cmd.Dir, cmd.Env = wd, env
			out, err := cmd.CombinedOutput()
			timedOut := ctx.Err() == context.DeadlineExceeded
			cancel()
			if timedOut {
				t.Fatalf("%s %v: still running after 30 s: %s", r.name, partial, out)
			}
			if err == nil || !strings.Contains(string(out), "refusing to run") {
				t.Fatalf("%s %v ran: %v\n%s", r.name, partial, err, out)
			}
			for _, d := range []string{home, wd} {
				ents, _ := os.ReadDir(d)
				for _, e := range ents {
					// pwsh itself makes AppData under the profile it is
					// given; what the script would write is checked below
					if runtime.GOOS == "windows" && d == home && e.Name() == "AppData" {
						continue
					}
					t.Fatalf("%s %v wrote into %s: %v", r.name, partial, d, ents)
				}
			}
			for _, d := range []string{filepath.Join(local, "AIUsage"), filepath.Join(local, "Programs", "AIUsage"),
				filepath.Join(home, "AppData", "Local", "AIUsage"), filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"),
				filepath.Join(home, ".codex")} {
				if _, err := os.Stat(d); err == nil {
					t.Fatalf("%s %v made %s", r.name, partial, d)
				}
			}
		}
	}
	if runtime.GOOS == "windows" {
		if after, _ := os.ReadFile(hostsFile); !bytes.Equal(after, hostsBefore) {
			t.Fatal("the hosts file changed")
		}
	}
}
