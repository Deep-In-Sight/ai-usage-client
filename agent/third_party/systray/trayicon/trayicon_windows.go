//go:build windows

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md).

// Package trayicon loads the Windows tray icon at the size the notification
// area draws it. It is a public package so that the AI Usage agent can see
// when that size changes (a display-scale change) and set its icon again.
package trayicon

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("User32.dll")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi")
	pMonitorFromPoint       = user32.NewProc("MonitorFromPoint")
	pLoadImage              = user32.NewProc("LoadImageW")
	pGetDpiForMonitor       = windows.NewLazySystemDLL("Shcore.dll").NewProc("GetDpiForMonitor")
)

// Size is the size the notification area draws icons at: the small-icon
// size (SM_CXSMICON) at the effective DPI of the primary monitor, which
// holds the notification area. In a process that is not per-monitor DPI
// aware, or without the per-DPI functions, it is GetSystemMetrics' value.
func Size() (cx, cy int32) {
	const SM_CXSMICON = 49
	const SM_CYSMICON = 50
	const MONITOR_DEFAULTTOPRIMARY = 1
	const MDT_EFFECTIVE_DPI = 0
	if pGetSystemMetricsForDpi.Find() == nil && pMonitorFromPoint.Find() == nil && pGetDpiForMonitor.Find() == nil {
		// MonitorFromPoint takes a POINT by value: (0, 0), the primary
		// monitor's origin, is one zero word on 64-bit and two on 32-bit.
		var mon uintptr
		if unsafe.Sizeof(uintptr(0)) == 8 {
			mon, _, _ = pMonitorFromPoint.Call(0, MONITOR_DEFAULTTOPRIMARY)
		} else {
			mon, _, _ = pMonitorFromPoint.Call(0, 0, MONITOR_DEFAULTTOPRIMARY)
		}
		var dpiX, dpiY uint32
		if mon != 0 {
			r, _, _ := pGetDpiForMonitor.Call(mon, MDT_EFFECTIVE_DPI, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY)))
			if r == 0 && dpiX > 0 && dpiY > 0 {
				x, _, _ := pGetSystemMetricsForDpi.Call(SM_CXSMICON, uintptr(dpiX))
				y, _, _ := pGetSystemMetricsForDpi.Call(SM_CYSMICON, uintptr(dpiY))
				if int32(x) > 0 && int32(y) > 0 {
					return int32(x), int32(y)
				}
			}
		}
	}
	x, _, _ := pGetSystemMetrics.Call(SM_CXSMICON)
	y, _, _ := pGetSystemMetrics.Call(SM_CYSMICON)
	return int32(x), int32(y)
}

// Load loads the icon file path at cx×cy: from an ICO, the entry of that
// size when there is one (LoadImage scales only when there is none).
func Load(path string, cx, cy int32) (windows.Handle, error) {
	const IMAGE_ICON = 1
	const LR_LOADFROMFILE = 0x00000010
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	res, _, err := pLoadImage.Call(0, uintptr(unsafe.Pointer(p)), IMAGE_ICON, uintptr(cx), uintptr(cy), LR_LOADFROMFILE)
	if res == 0 {
		return 0, err
	}
	return windows.Handle(res), nil
}
