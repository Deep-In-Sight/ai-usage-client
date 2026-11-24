//go:build linux || darwin

package procs

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func init() {
	// AI_USAGE_HELPER=hold: hold the lock at AI_USAGE_HELPER_LOCK, with this
	// process's identity unless AI_USAGE_HELPER_NOID is set, print
	// "locked", and exit on SIGTERM unless AI_USAGE_HELPER_IGNORE is set.
	helpers["hold"] = func() {
		if os.Getenv("AI_USAGE_HELPER_IGNORE") != "" {
			signal.Ignore(syscall.SIGTERM)
		} else {
			signal.Reset(syscall.SIGTERM)
		}
		path := os.Getenv("AI_USAGE_HELPER_LOCK")
		var err error
		lock := func() error {
			if os.Getenv("AI_USAGE_HELPER_NOID") != "" {
				_, err := Lock(path)
				return err
			}
			id, err := Self()
			if err == nil {
				_, err = LockAs(path, id)
			}
			return err
		}
		if os.Getenv("AI_USAGE_HELPER_QUEUE") != "" {
			// a run that waits for the lock (like a second login)
			os.Stdout.WriteString("waiting\n")
			for err = lock(); errors.Is(err, ErrLocked); err = lock() {
				time.Sleep(20 * time.Millisecond)
			}
		} else {
			err = lock()
		}
		if err != nil {
			os.Stdout.WriteString("error " + err.Error() + "\n")
			os.Exit(3)
		}
		os.Stdout.WriteString("locked\n")
		time.Sleep(60 * time.Second)
	}
}

type holder struct {
	cmd  *exec.Cmd
	done chan error
}

// startHolder starts the test binary as a process holding the lock at path.
func startHolder(t *testing.T, path string, extra ...string) *holder {
	t.Helper()
	return startHelper(t, "locked", path, extra...)
}

// startHelper starts the hold helper and waits for its first line.
func startHelper(t *testing.T, first, path string, extra ...string) *holder {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), append([]string{"AI_USAGE_HELPER=hold", "AI_USAGE_HELPER_LOCK=" + path}, extra...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &holder{cmd: cmd, done: make(chan error, 1)}
	line, err := bufio.NewReader(out).ReadString('\n')
	go func() { h.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
		}
	})
	if err != nil || strings.TrimSpace(line) != first {
		t.Fatalf("holder: %q %v", line, err)
	}
	return h
}

func (h *holder) exited(within time.Duration) (error, bool) {
	select {
	case err := <-h.done:
		h.done <- err
		return err, true
	case <-time.After(within):
		return nil, false
	}
}

// newest is an identity newer than any process here.
func newest() Identity { return Identity{PID: os.Getpid(), Start: ^uint64(0)} }

var quick = TakeOverOptions{TermWait: 3 * time.Second, KillWait: 2 * time.Second}

func TestSelfIdentity(t *testing.T) {
	me, err := Self()
	if err != nil {
		t.Fatal(err)
	}
	if me.PID != os.Getpid() || me.Start == 0 {
		t.Fatalf("%+v", me)
	}
	again, err := IdentityOf(os.Getpid())
	if err != nil || again != me {
		t.Fatalf("%+v %v", again, err)
	}
	lp := filepath.Join(t.TempDir(), "run.lock")
	h := startHolder(t, lp)
	child, err := IdentityOf(h.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !me.Older(child) || child.Older(me) {
		t.Fatalf("me %+v child %+v", me, child)
	}
	// the lock file names the holder
	got, ok := Holder(lp)
	if !ok || got != child {
		t.Fatalf("holder %+v %v, want %+v", got, ok, child)
	}
	_ = h.cmd.Process.Kill()
	h.exited(5 * time.Second)
	if _, err := IdentityOf(h.cmd.Process.Pid); err == nil {
		t.Fatal("identity of an ended process")
	}
}

func TestOlderIsATotalOrder(t *testing.T) {
	a, b := Identity{PID: 10, Start: 5}, Identity{PID: 9, Start: 6}
	if !a.Older(b) || b.Older(a) {
		t.Fatal("start time first")
	}
	c := Identity{PID: 11, Start: 5}
	if !a.Older(c) || c.Older(a) || a.Older(a) {
		t.Fatal("pid breaks ties")
	}
}

func TestTakeOverFreeLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	me, _ := Self()
	unlock, replaced, err := TakeOver(context.Background(), p, me, quick)
	if err != nil || replaced != (Identity{}) {
		t.Fatalf("%+v %v", replaced, err)
	}
	if got, ok := Holder(p); !ok || got != me {
		t.Fatalf("lock file names %+v %v", got, ok)
	}
	unlock()
}

// A newer run asks the older holder to stop (SIGTERM) and takes the lock.
func TestTakeOverAsksTheOlderHolderToStop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	h := startHolder(t, p)
	old, _ := IdentityOf(h.cmd.Process.Pid)
	start := time.Now()
	unlock, replaced, err := TakeOver(context.Background(), p, newest(), quick)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if replaced != old {
		t.Fatalf("replaced %+v, want %+v", replaced, old)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if _, ok := h.exited(2 * time.Second); !ok {
		t.Fatal("the old holder still runs")
	}
	if got, _ := Holder(p); got != newest() {
		t.Fatalf("lock file names %+v", got)
	}
}

// A holder that does not stop on SIGTERM (wedged) is killed after TermWait.
func TestTakeOverKillsAHolderThatIgnoresTerm(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	h := startHolder(t, p, "AI_USAGE_HELPER_IGNORE=1")
	o := TakeOverOptions{TermWait: 300 * time.Millisecond, KillWait: 3 * time.Second}
	unlock, _, err := TakeOver(context.Background(), p, newest(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	err, ok := h.exited(2 * time.Second)
	if !ok {
		t.Fatal("still running")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("ended with %v, want SIGKILL", err)
	}
}

// A holder newer than this run (two logins in quick succession, or a run
// that restarted itself after the newer one started) is left alone.
func TestTakeOverLeavesANewerHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	h := startHolder(t, p)
	me, _ := Self() // started before the holder
	child, _ := IdentityOf(h.cmd.Process.Pid)
	start := time.Now()
	_, holder, err := TakeOver(context.Background(), p, me, quick)
	if !errors.Is(err, ErrNewer) || holder != child {
		t.Fatalf("%+v %v", holder, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	if _, gone := h.exited(300 * time.Millisecond); gone {
		t.Fatal("the newer holder was stopped")
	}
}

// A holder whose identity is not in the lock file (an older agent) is never
// signalled; the run gives up after the wait.
func TestTakeOverNeverSignalsAnUnknownHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	h := startHolder(t, p, "AI_USAGE_HELPER_NOID=1")
	o := TakeOverOptions{TermWait: 300 * time.Millisecond, KillWait: 300 * time.Millisecond}
	start := time.Now()
	_, _, err := TakeOver(context.Background(), p, newest(), o)
	if !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 600*time.Millisecond || d > 3*time.Second {
		t.Fatalf("gave up after %v", d)
	}
	if _, gone := h.exited(200 * time.Millisecond); gone {
		t.Fatal("unknown holder was stopped")
	}
}

// A stale identity (its pid now names another process) is not signalled.
func TestTakeOverIgnoresAStaleIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	h := startHolder(t, p, "AI_USAGE_HELPER_NOID=1")
	child, _ := IdentityOf(h.cmd.Process.Pid)
	stale := Identity{PID: child.PID, Start: child.Start - 1}
	if err := os.WriteFile(p, []byte(stale.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Holder(p); ok {
		t.Fatal("stale identity taken as the holder")
	}
	o := TakeOverOptions{TermWait: 200 * time.Millisecond, KillWait: 200 * time.Millisecond}
	if _, _, err := TakeOver(context.Background(), p, newest(), o); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if _, gone := h.exited(200 * time.Millisecond); gone {
		t.Fatal("stopped through a stale identity")
	}
}

// The wait ends with the context (SIGTERM from uninstall during a
// hand-over).
func TestTakeOverEndsWithTheContext(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	startHolder(t, p, "AI_USAGE_HELPER_IGNORE=1")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _, err := TakeOver(ctx, p, newest(), TakeOverOptions{TermWait: 10 * time.Second, KillWait: 5 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

// Two runs queue up behind an old holder (two logins in quick succession,
// the older of the two newcomers waiting for the lock): whichever older run
// gets the lock next is asked to stop too, and the newest ends up holding it.
func TestTakeOverAlsoStopsTheNextOlderHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	first := startHolder(t, p)
	second := startHelper(t, "waiting", p, "AI_USAGE_HELPER_QUEUE=1")
	unlock, replaced, err := TakeOver(context.Background(), p, newest(), quick)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, ok := first.exited(2 * time.Second); !ok {
		t.Fatal("first holder still runs")
	}
	if _, ok := second.exited(2 * time.Second); !ok {
		// the second may not have got the lock before this run did; then it
		// is still waiting and must be left alone, which is fine too
		if replaced.PID == second.cmd.Process.Pid {
			t.Fatal("second holder was asked to stop but still runs")
		}
	}
	if got, _ := Holder(p); got != newest() {
		t.Fatalf("lock file names %+v", got)
	}
}
