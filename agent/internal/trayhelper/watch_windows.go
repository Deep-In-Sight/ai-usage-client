package trayhelper

import (
	"context"
	"log"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// watchSession waits for Windows' TaskbarCreated broadcast: Explorer's
// taskbar started (late at login, or again after Explorer ended).
var watchSession = waitTaskbarCreated

var (
	watchUser32             = windows.NewLazySystemDLL("user32.dll")
	pRegisterWindowMessageW = watchUser32.NewProc("RegisterWindowMessageW")
	pRegisterClassExW       = watchUser32.NewProc("RegisterClassExW")
	pCreateWindowExW        = watchUser32.NewProc("CreateWindowExW")
	pDestroyWindow          = watchUser32.NewProc("DestroyWindow")
	pDefWindowProcW         = watchUser32.NewProc("DefWindowProcW")
	pGetMessageW            = watchUser32.NewProc("GetMessageW")
	pTranslateMessage       = watchUser32.NewProc("TranslateMessage")
	pDispatchMessageW       = watchUser32.NewProc("DispatchMessageW")
	pPostMessageW           = watchUser32.NewProc("PostMessageW")
	pPostQuitMessage        = watchUser32.NewProc("PostQuitMessage")
	pGetModuleHandleW       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

const (
	watchClass = "AIUsageDesktopWatch"
	wmDestroy  = 0x0002
	wmClose    = 0x0010
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

type winMsg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

var (
	watchOnce      sync.Once
	watchErr       error
	taskbarCreated uint32
	watchMu        sync.Mutex
	watchSeen      = map[uintptr]*bool{} // window: TaskbarCreated came
)

func watchProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch {
	case taskbarCreated != 0 && uint32(msg) == taskbarCreated:
		watchMu.Lock()
		if seen := watchSeen[hwnd]; seen != nil {
			*seen = true
		}
		watchMu.Unlock()
		_, _, _ = pDestroyWindow.Call(hwnd)
		return 0
	case msg == wmClose:
		_, _, _ = pDestroyWindow.Call(hwnd)
		return 0
	case msg == wmDestroy:
		_, _, _ = pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// registerWatch registers the TaskbarCreated message and the hidden
// window's class, once.
func registerWatch() error {
	watchOnce.Do(func() {
		name, _ := windows.UTF16PtrFromString("TaskbarCreated")
		r, _, err := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
		if r == 0 {
			watchErr = err
			return
		}
		taskbarCreated = uint32(r)
		inst, _, _ := pGetModuleHandleW.Call(0)
		cls, _ := windows.UTF16PtrFromString(watchClass)
		wc := wndClassEx{wndProc: windows.NewCallback(watchProc), instance: windows.Handle(inst), className: cls}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			watchErr = err
		}
	})
	return watchErr
}

// waitTaskbarCreated waits in a hidden top-level window of its own (a
// message-only window gets no broadcasts) for TaskbarCreated. False when
// ctx ends or no window could be made.
func waitTaskbarCreated(ctx context.Context) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := registerWatch(); err != nil {
		log.Printf("desktop watch: %v", err)
		return false
	}
	inst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString(watchClass)
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		log.Printf("desktop watch: %v", err)
		return false
	}
	seen := false
	watchMu.Lock()
	watchSeen[hwnd] = &seen
	watchMu.Unlock()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_, _, _ = pPostMessageW.Call(hwnd, wmClose, 0, 0)
		case <-done:
		}
	}()
	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT, or an error
			break
		}
		_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	close(done)
	watchMu.Lock()
	defer watchMu.Unlock()
	delete(watchSeen, hwnd)
	return seen
}
