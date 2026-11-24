package supervisor

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

const (
	helperEnv     = "SUPERVISOR_TEST_ACT_AS_HELPER"
	helperModeEnv = "SUPERVISOR_TEST_HELPER_MODE" // "die": end with 3 right after the first line
)

// TestMain lets this test binary stand in for a helper process.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(actAsHelper())
	}
	os.Exit(m.Run())
}

// actAsHelper is a minimal helper: it checks the first line against the
// token, says on standard error what it was started with, reports ready,
// clicks "refresh" and quits on "quit".
func actAsHelper() int {
	r := bufio.NewReader(os.Stdin)
	first, err := trayproto.Decode(r)
	if err != nil || first.Type != trayproto.TypeHello || first.Token != os.Getenv(trayproto.EnvToken) {
		return trayproto.ExitRefused
	}
	if os.Getenv(helperModeEnv) == "die" {
		return 3
	}
	fmt.Fprintf(os.Stderr, "args %s\nsecond line\npartial", strings.Join(os.Args[1:], " "))
	_ = trayproto.Encode(os.Stdout, trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeReady})
	_ = trayproto.Encode(os.Stdout, trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeClick, ID: "refresh"})
	for {
		m, err := trayproto.Decode(r)
		if err != nil {
			return trayproto.ExitOK
		}
		if m.Type == trayproto.TypeQuit {
			return 7 // a code the test can tell
		}
	}
}

func TestExecSpawnerRunsTheHelper(t *testing.T) {
	t.Setenv(helperEnv, "1")
	logBuf := &syncBuffer{}
	sup := New(Options{
		Spawn: ExecSpawner(os.Args[0], func(p provider.ID) string { return "/icons/" + string(p) }, log.New(logBuf, "", 0)),
		Clock: clock.Real{},
		Log:   log.New(logBuf, "", 0),
	})
	defer sup.Stop()
	sup.Want([]provider.ID{provider.Codex})
	var got []Event
	deadline := time.After(20 * time.Second)
	for len(got) < 2 {
		select {
		case e := <-sup.Events():
			if e.Exit != nil {
				t.Fatalf("helper ended: %d; log: %s", *e.Exit, logBuf.String())
			}
			got = append(got, e)
		case <-deadline:
			t.Fatalf("events %+v; log: %s", got, logBuf.String())
		}
	}
	if !got[0].Ready || got[1].Click != "refresh" || got[1].Provider != provider.Codex {
		t.Fatalf("%+v", got)
	}
	done := make(chan struct{})
	go func() { sup.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Stop did not return")
	}
	logged := logBuf.String()
	for _, want := range []string{
		"[tray codex] args tray-helper --provider codex --uid 101 --icon-dir /icons/codex\n",
		"[tray codex] second line\n",
		"[tray codex] partial\n",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log lacks %q:\n%s", want, logged)
		}
	}
}

func TestExecSpawnerRefusedWithoutToken(t *testing.T) {
	t.Setenv(helperEnv, "1")
	spawn := ExecSpawner(os.Args[0], func(provider.ID) string { return "" }, log.New(&syncBuffer{}, "", 0))
	p, err := spawn("", 100, "the-right-token")
	if err != nil {
		t.Fatal(err)
	}
	// a wrong first line
	if err := trayproto.Encode(p.Stdin(), trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeHello, Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	code, _ := p.Wait()
	if code != trayproto.ExitRefused {
		t.Fatalf("exit %d", code)
	}
}

func TestHelperArgs(t *testing.T) {
	got := strings.Join(HelperArgs("", 100, "x"), " ")
	if got != "tray-helper --provider none --uid 100 --icon-dir x" {
		t.Fatal(got)
	}
}

func TestLineLoggerCutsLongLines(t *testing.T) {
	b := &syncBuffer{}
	l := &lineLogger{lg: log.New(b, "", 0), prefix: "[p] "}
	_, _ = l.Write([]byte(strings.Repeat("a", 5000) + "\n\nb\r\n"))
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 2 || len(lines[0]) != 4+maxLogLine || lines[1] != "[p] b" { // empty lines are left out
		t.Fatalf("%d lines: %q", len(lines), lines)
	}
}

// openFiles is how many files this process has open (Linux).
func openFiles(t *testing.T) int {
	t.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(fds)
}

// settledOpenFiles waits until the count stays the same for a while.
func settledOpenFiles(t *testing.T) int {
	t.Helper()
	last := openFiles(t)
	for range 20 {
		time.Sleep(50 * time.Millisecond)
		n := openFiles(t)
		if n == last {
			return n
		}
		last = n
	}
	return last
}

// A dead helper's pipes are closed by the agent, not left to the garbage
// collector (stage-2 review m-1).
func TestDeadHelpersLeaveNoOpenFiles(t *testing.T) {
	if _, err := os.ReadDir("/proc/self/fd"); err != nil {
		t.Skip("counts open files through /proc/self/fd")
	}
	t.Setenv(helperEnv, "1")
	t.Setenv(helperModeEnv, "die")
	defer debug.SetGCPercent(debug.SetGCPercent(-1)) // only what the agent closes counts
	clk := clock.NewFake(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC))
	logBuf := &syncBuffer{}
	sup := New(Options{
		Spawn: ExecSpawner(os.Args[0], func(provider.ID) string { return t.TempDir() }, log.New(logBuf, "", 0)),
		Clock: clk,
		Log:   log.New(logBuf, "", 0),
	})
	defer sup.Stop()
	died := func() {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case e := <-sup.Events():
				if e.Exit != nil {
					return
				}
			case <-deadline:
				t.Fatalf("the helper did not end; log: %s", logBuf.String())
			}
		}
	}
	sup.Want([]provider.ID{provider.Claude})
	died() // the first death opens what the runtime keeps open
	before := settledOpenFiles(t)
	for range 4 {
		clk.Advance(time.Minute) // its restart
		died()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := openFiles(t)
		if n <= before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d files open after four more helpers died, %d before", n, before)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
