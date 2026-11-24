package trayhelper

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

const realHelperEnv = "AI_USAGE_TEST_REAL_HELPER_CHILD"

// TestRealHelperChild is the child of TestRealHelpersReadyNamesItsWindow:
// a real helper (the real tray library) on this desktop.
func TestRealHelperChild(t *testing.T) {
	if os.Getenv(realHelperEnv) == "" {
		t.Skip("child of TestRealHelpersReadyNamesItsWindow")
	}
	os.Exit(Run(context.Background(), os.Stdin, os.Stdout, "tok-0123456789"))
}

// Review N-5: a real helper's ready message names its own icon window (a
// SystrayClass window of the helper's process), which the next helper
// needs to remove a crashed helper's icon.
func TestRealHelpersReadyNamesItsWindow(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRealHelperChild$")
	cmd.Env = append(os.Environ(), realHelperEnv+"=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	send := func(m trayproto.Msg) {
		m.V = trayproto.Version
		if err := trayproto.Encode(in, m); err != nil {
			t.Fatal(err)
		}
	}
	send(trayproto.Msg{Type: trayproto.TypeHello, Token: "tok-0123456789", Provider: "claude", UID: 4245, IconDir: t.TempDir()})
	send(view("42%", false))
	msgs := make(chan trayproto.Msg, 16)
	go func() {
		r := bufio.NewReader(out)
		for {
			m, err := trayproto.Decode(r)
			if err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("the helper ended before it was ready")
			}
			if m.Type == trayproto.TypeWaiting {
				t.Skipf("no notification area here: %s", m.Why)
			}
			if m.Type != trayproto.TypeReady {
				continue
			}
			if m.Window == 0 {
				t.Fatal("ready names no window")
			}
			var pid uint32
			pGetWindowThreadProcessID.Call(uintptr(m.Window), uintptr(unsafe.Pointer(&pid)))
			if int(pid) != cmd.Process.Pid {
				t.Fatalf("window %d belongs to process %d, the helper is %d", m.Window, pid, cmd.Process.Pid)
			}
			buf := make([]uint16, 64)
			n, _, _ := watchUser32.NewProc("GetClassNameW").Call(uintptr(m.Window), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			if got := windows.UTF16ToString(buf[:n]); got != systrayClass {
				t.Fatalf("window class %q", got)
			}
			send(trayproto.Msg{Type: trayproto.TypeQuit})
			in.Close()
			return
		case <-deadline:
			t.Fatal("no ready within 60 s")
		}
	}
}
