package notify

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	pCreateWindowEx  = user32.NewProc("CreateWindowExW")
	pDestroyWindow   = user32.NewProc("DestroyWindow")
	pLoadIcon        = user32.NewProc("LoadIconW")
	pShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
)

const (
	nimAdd          = 0x0
	nimDelete       = 0x2
	nifIcon         = 0x2
	nifTip          = 0x4
	nifInfo         = 0x10
	niifInfo        = 0x1
	idiInformation  = 32516
	balloonLifetime = 15 * time.Second
)

// hwndMessage is HWND_MESSAGE ((HWND)-3): a message-only window.
var hwndMessage = ^uintptr(2)

// notifyIconData has the layout of fyne.io/systray's (NOTIFYICONDATAW).
type notifyIconData struct {
	Size                       uint32
	Wnd                        windows.Handle
	ID, Flags, CallbackMessage uint32
	Icon                       windows.Handle
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	TimeoutOrVersion           uint32 // uTimeout and uVersion are a union
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GuidItem                   windows.GUID
	BalloonIcon                windows.Handle
}

func fill(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// Show puts a second notification-area icon up with a balloon (shown as a
// toast on Windows 10 and 11) and removes it after 15 s. It returns once the
// balloon is up.
func Show(title, body string) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		inst, _, _ := pGetModuleHandle.Call(0)
		class, _ := windows.UTF16PtrFromString("STATIC")
		hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, inst, 0)
		if hwnd == 0 {
			errc <- err
			return
		}
		defer pDestroyWindow.Call(hwnd)
		app, _ := windows.UTF16PtrFromString("APP")
		ico, _, _ := pLoadIcon.Call(inst, uintptr(unsafe.Pointer(app)))
		if ico == 0 {
			ico, _, _ = pLoadIcon.Call(0, idiInformation)
		}
		nid := notifyIconData{Wnd: windows.Handle(hwnd), ID: 1, Flags: nifIcon | nifTip | nifInfo, Icon: windows.Handle(ico), InfoFlags: niifInfo}
		nid.Size = uint32(unsafe.Sizeof(nid))
		fill(nid.Tip[:], title)
		fill(nid.InfoTitle[:], title)
		fill(nid.Info[:], body)
		if r, _, err := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
			if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
				err = errors.New("notify: Shell_NotifyIcon failed")
			}
			errc <- err
			return
		}
		errc <- nil
		time.Sleep(balloonLifetime)
		pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	}()
	return <-errc
}
