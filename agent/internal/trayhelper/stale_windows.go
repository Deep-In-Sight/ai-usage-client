package trayhelper

import (
	"log"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// A notification icon belongs to a window and an id. When its process ends
// without removing it (a crash, a kill), the icon stays in the
// notification area until the pointer passes over it. The next helper
// removes such icons by their window and id (review n-2).

var (
	pShellNotifyIconW         = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	pFindWindowExW            = watchUser32.NewProc("FindWindowExW")
	pGetWindowThreadProcessID = watchUser32.NewProc("GetWindowThreadProcessId")
)

// notifyIconDataW is NOTIFYICONDATAW (shellapi.h).
type notifyIconDataW struct {
	size            uint32
	wnd             windows.Handle
	id              uint32
	flags           uint32
	callbackMessage uint32
	icon            windows.Handle
	tip             [128]uint16
	state           uint32
	stateMask       uint32
	info            [256]uint16
	timeoutVersion  uint32
	infoTitle       [64]uint16
	infoFlags       uint32
	guidItem        windows.GUID
	balloonIcon     windows.Handle
}

const (
	nimAdd    = 0x0
	nimDelete = 0x2
	nifIcon   = 0x2
)

func shellNotifyIcon(msg uintptr, nid *notifyIconDataW) bool {
	nid.size = uint32(unsafe.Sizeof(*nid))
	r, _, _ := pShellNotifyIconW.Call(msg, uintptr(unsafe.Pointer(nid)))
	return r != 0
}

// removeIcon removes the notification icon of window and id; false when
// the notification area has none.
func removeIcon(window uint64, id uint32) bool {
	return shellNotifyIcon(nimDelete, &notifyIconDataW{wnd: windows.Handle(window), id: id})
}

// removeStaleIcons removes the icons helpers left behind.
func removeStaleIcons(stale []trayproto.StaleIcon) {
	for _, s := range stale {
		if s.Window == 0 {
			continue
		}
		if removeIcon(s.Window, s.UID) {
			log.Printf("removed a notification icon a helper had left (id %d)", s.UID)
		}
	}
}

// systrayClass is the window class of the tray library's hidden window,
// which owns the notification icon.
const systrayClass = "SystrayClass"

// iconWindow is this process's tray window (0 when there is none).
func iconWindow() uint64 {
	cls, _ := windows.UTF16PtrFromString(systrayClass)
	self := uint32(os.Getpid())
	var after uintptr
	for {
		w, _, _ := pFindWindowExW.Call(0, after, uintptr(unsafe.Pointer(cls)), 0)
		if w == 0 {
			return 0
		}
		var pid uint32
		_, _, _ = pGetWindowThreadProcessID.Call(w, uintptr(unsafe.Pointer(&pid)))
		if pid == self {
			return uint64(w)
		}
		after = w
	}
}
