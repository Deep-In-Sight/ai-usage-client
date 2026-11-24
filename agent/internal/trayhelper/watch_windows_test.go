package trayhelper

import (
	"context"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pFindWindowW        = watchUser32.NewProc("FindWindowW")
	pSendNotifyMessageW = watchUser32.NewProc("SendNotifyMessageW")
)

// waitWatchWindow waits until the watch's hidden window exists.
func waitWatchWindow(t *testing.T) {
	t.Helper()
	cls, _ := windows.UTF16PtrFromString(watchClass)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if h, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0); h != 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no watch window")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TaskbarCreated, broadcast to every top-level window as Explorer does
// when its taskbar starts, ends the watch with true.
func TestTaskbarCreatedEndsTheWatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- waitTaskbarCreated(ctx) }()
	waitWatchWindow(t)
	select {
	case ok := <-done:
		t.Fatalf("ended (%v) before the broadcast", ok)
	case <-time.After(100 * time.Millisecond):
	}
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	msg, _, err := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	if msg == 0 {
		t.Fatal(err)
	}
	const hwndBroadcast = 0xffff
	if r, _, err := pSendNotifyMessageW.Call(hwndBroadcast, msg, 0, 0); r == 0 {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("false")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TaskbarCreated was not noticed")
	}
}

func TestDesktopWatchEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- waitTaskbarCreated(ctx) }()
	waitWatchWindow(t)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("true without TaskbarCreated")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not end")
	}
}
