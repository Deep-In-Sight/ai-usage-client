package trayhelper

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The real connection on a private session bus: a watcher that takes its
// name later is noticed at once. Needs dbus-daemon (skipped without it).
func TestWaitForHostOnAPrivateBus(t *testing.T) {
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(stdout).ReadString('\n')
	must(t, err)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- WaitForHost(ctx, 0) }()
	select {
	case ok := <-done:
		t.Fatalf("returned %v before the watcher came", ok)
	case <-time.After(500 * time.Millisecond):
	}
	conn, err := dbus.ConnectSessionBus()
	must(t, err)
	defer conn.Close()
	start := time.Now()
	reply, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue)
	must(t, err)
	eq(t, reply, dbus.RequestNameReplyPrimaryOwner)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("false")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("noticed after %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher was not noticed")
	}
}

// The real watch on a private session bus: a watcher taking its name ends
// it at once. Needs dbus-daemon (skipped without it).
func TestWatchSessionOnAPrivateBus(t *testing.T) {
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(stdout).ReadString('\n')
	must(t, err)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- watchSession(ctx) }()
	select {
	case ok := <-done:
		t.Fatalf("returned %v before the watcher came", ok)
	case <-time.After(500 * time.Millisecond):
	}
	conn, err := dbus.ConnectSessionBus()
	must(t, err)
	defer conn.Close()
	_, err = conn.RequestName(watcherName, dbus.NameFlagDoNotQueue)
	must(t, err)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher was not noticed")
	}
}
