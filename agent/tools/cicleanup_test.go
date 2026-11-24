//go:build linux

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Re-review R-3: the release check's cleanup ends every agent and tray
// helper of the installed executable (one that ignores SIGTERM too)
// before it gives the real host names back, then restores the CA and the
// sysctl. sudo, systemctl, sysctl and update-ca-certificates are fakes
// that only write down what they were asked (and whether an agent still
// ran at that moment): nothing on this machine is changed.
func TestReleaseCleanupEndsAgentsFirst(t *testing.T) {
	tmp := t.TempDir()
	bin, inst, ws, rt := filepath.Join(tmp, "bin"), filepath.Join(tmp, "inst"), filepath.Join(tmp, "ws"), filepath.Join(tmp, "rt")
	for _, d := range []string{bin, inst, ws, rt} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(tmp, "calls.log")
	exe := filepath.Join(inst, "ai-usage")
	fake := "#!/bin/sh\necho \"$(basename \"$0\") $* agents=$(pgrep -f \"^" + exe + " (run|tray-helper)\" | wc -l | tr -d ' ')\" >> " + calls + "\n"
	for _, n := range []string{"sudo", "systemctl", "sysctl", "update-ca-certificates"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// the "agent": bash under the installed name, running the script "run"
	// (its command line is "<exe> run") and ignoring SIGTERM
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	b, err := os.ReadFile(bashPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inst, "run"), []byte("trap '' TERM\nwhile :; do sleep 1; done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := exec.Command(exe, "run")
	agent.Dir = inst
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = agent.Wait(); close(done) }()
	t.Cleanup(func() { _ = agent.Process.Kill() })

	ciDir, _ := filepath.Abs("../ci")
	saved := filepath.Join(rt, "hosts.saved")
	if err := os.WriteFile(saved, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := `. "$CI_DIR/lib.sh"
exe="$EXE"
REL_HOSTS_SAVED="$SAVED" REL_CA=/nonexistent/ai-usage-ci-fakevendor.crt REL_PORT_OLD=1024
release_cleanup
echo "left=$(pgrep -f "^$exe (run|tray-helper)" | wc -l | tr -d ' ')"`
	cmd := exec.Command("bash", "-c", sh)
	cmd.Dir = tmp
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + tmp, "GITHUB_ACTIONS=true", "RUNNER_TEMP=" + rt,
		"GITHUB_WORKSPACE=" + ws, "CI_DIR=" + ciDir, "EXE=" + exe, "SAVED=" + saved}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "left=0") {
		t.Fatalf("%v\n%s", err, out)
	}
	<-done // the agent was ended
	log, _ := os.ReadFile(calls)
	got := string(log)
	for _, want := range []string{"systemctl --user stop ai-usage.service agents=1",
		"sudo cp " + saved + " /etc/hosts agents=0",
		"sudo rm -f /nonexistent/ai-usage-ci-fakevendor.crt agents=0",
		"sudo sysctl -q -w net.ipv4.ip_unprivileged_port_start=1024 agents=0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q not among the calls:\n%s", want, got)
		}
	}
	if strings.Index(got, "/etc/hosts") < strings.Index(got, "systemctl --user stop") {
		t.Fatalf("names given back before the agents were stopped:\n%s", got)
	}
}
