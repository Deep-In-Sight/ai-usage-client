//go:build windows

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

import (
	"log"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	odMonitorFromPoint = u32.NewProc("MonitorFromPoint")
	odGetDpiForWindow  = u32.NewProc("GetDpiForWindow")
	odWindowFromDC     = u32.NewProc("WindowFromDC")
	odGetDpiForMonitor = windows.NewLazySystemDLL("Shcore.dll").NewProc("GetDpiForMonitor")
	odCreateSolidBrush = g32.NewProc("CreateSolidBrush")
	odDeleteObject     = g32.NewProc("DeleteObject")
)

const (
	odWMMeasureItem   = 0x002C
	odWMDrawItem      = 0x002B
	odWMSettingChange = 0x001A
	odODTMenu         = 1
	odMFTOwnerDraw    = 0x00000100
	odMIIMData        = 0x00000020
)

// od is what the owner-drawn items need: the drawer, which items and
// separators are owner-drawn, the DPI of the last measure and the menu's
// background brush.
var od struct {
	mu       sync.Mutex
	drawer   MenuDrawer
	items    map[uint32]bool
	seps     map[uint32]bool
	dpi      uint32
	brush    windows.Handle
	brushRGB uint32
}

// SetMenuDrawer sets the drawer of the owner-drawn items; before or after
// Run. nil: none (owner-drawn items are then left to the system, which
// draws nothing).
func SetMenuDrawer(d MenuDrawer) {
	od.mu.Lock()
	defer od.mu.Unlock()
	od.drawer = d
}

// SetOwnerDrawn makes the item owner-drawn (MFT_OWNERDRAW: the drawer
// measures and draws it; it has no text or bitmap of its own in the menu)
// or, with false, a text item again. A hidden item stays hidden.
func (item *MenuItem) SetOwnerDrawn(on bool) {
	od.mu.Lock()
	if od.items == nil {
		od.items = map[uint32]bool{}
	}
	if on {
		od.items[item.id] = true
	} else {
		delete(od.items, item.id)
	}
	od.mu.Unlock()
	if wt.isReady() && wt.getVisibleItemIndex(item.parentId(), item.id) != -1 {
		addOrUpdateMenuItem(item)
	}
}

// MenuOwnerWindow is the tray's hidden window, which owns the menu and
// gets the tray icon's messages (0 before Run).
func MenuOwnerWindow() uintptr { return uintptr(wt.window) }

// AddOwnerDrawnSeparator adds a separator that the drawer draws (its
// MenuDecorator side); without one, the system draws nothing there.
func AddOwnerDrawnSeparator() {
	id := currentID.Add(1)
	od.mu.Lock()
	if od.seps == nil {
		od.seps = map[uint32]bool{}
	}
	od.seps[id] = true
	od.mu.Unlock()
	addSeparator(id, 0)
}

func isOwnerDrawn(id uint32) bool {
	od.mu.Lock()
	defer od.mu.Unlock()
	return od.items[id]
}

func isOwnerDrawnSeparator(id uint32) bool {
	od.mu.Lock()
	defer od.mu.Unlock()
	return od.seps[id]
}

// forgetOwnerDrawn: the menu was reset, its items and separators are gone.
func forgetOwnerDrawn() {
	od.mu.Lock()
	defer od.mu.Unlock()
	od.items, od.seps = nil, nil
}

type odMeasureItem struct {
	CtlType, CtlID, ItemID, ItemWidth, ItemHeight uint32
	ItemData                                      uintptr
}

type odDrawItem struct {
	CtlType, CtlID, ItemID, ItemAction, ItemState uint32
	HwndItem                                      windows.Handle
	HDC                                           uintptr
	RcItem                                        Rect
	ItemData                                      uintptr
}

// ownerDrawMessage handles WM_MEASUREITEM and WM_DRAWITEM for the menu's
// owner-drawn items (handled: return result) and passes WM_SETTINGCHANGE
// to the drawer (not handled: the default procedure runs too).
func ownerDrawMessage(msg uint32, wParam, lParam uintptr) (handled bool, result uintptr) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("systray error: drawing a menu item: %v", r)
			handled, result = true, 1
		}
	}()
	od.mu.Lock()
	d := od.drawer
	od.mu.Unlock()
	if d == nil {
		return false, 0
	}
	switch msg {
	case odWMMeasureItem:
		mi := (*odMeasureItem)(unsafe.Pointer(lParam))
		if mi.CtlType != odODTMenu {
			return false, 0
		}
		dpi := cursorDPI()
		od.mu.Lock()
		od.dpi = dpi
		od.mu.Unlock()
		var w, h int32
		switch {
		case isOwnerDrawnSeparator(mi.ItemID):
			dec, ok := d.(MenuDecorator)
			if !ok {
				return false, 0
			}
			w, h = dec.MeasureSeparator(dpi)
		case isOwnerDrawn(mi.ItemID):
			w, h = d.Measure(mi.ItemID, dpi)
		default:
			return false, 0
		}
		if w < 0 {
			w = 0
		}
		if h < 0 {
			h = 0
		}
		mi.ItemWidth, mi.ItemHeight = uint32(w), uint32(h)
		return true, 1
	case odWMDrawItem:
		di := (*odDrawItem)(unsafe.Pointer(lParam))
		if di.CtlType != odODTMenu {
			return false, 0
		}
		dpi := windowDPI(di.HDC)
		switch {
		case isOwnerDrawnSeparator(di.ItemID):
			dec, ok := d.(MenuDecorator)
			if !ok {
				return false, 0
			}
			dec.DrawSeparator(di.HDC, di.RcItem, dpi)
		case isOwnerDrawn(di.ItemID):
			d.Draw(di.ItemID, di.HDC, di.RcItem, di.ItemState, dpi)
		default:
			return false, 0
		}
		return true, 1
	case odWMSettingChange:
		area := ""
		if lParam != 0 {
			area = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(lParam)))
		}
		d.SettingChanged(area)
	}
	return false, 0
}

// cursorDPI is the effective DPI of the monitor under the pointer (the
// menu opens there); 96 when Windows cannot tell.
func cursorDPI() uint32 {
	if odMonitorFromPoint.Find() != nil || odGetDpiForMonitor.Find() != nil {
		return 96
	}
	var p point
	if r, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		return 96
	}
	const monitorDefaultToNearest, mdtEffectiveDPI = 2, 0
	mon, _, _ := odMonitorFromPoint.Call(uintptr(uint32(p.X))|uintptr(uint32(p.Y))<<32, monitorDefaultToNearest)
	var x, y uint32
	if hr, _, _ := odGetDpiForMonitor.Call(mon, mdtEffectiveDPI, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y))); hr != 0 || x == 0 {
		return 96
	}
	return x
}

// windowDPI is the DPI of the window hdc draws in (the menu's), else the
// last measure's, else 96.
func windowDPI(hdc uintptr) uint32 {
	if odWindowFromDC.Find() == nil && odGetDpiForWindow.Find() == nil {
		if w, _, _ := odWindowFromDC.Call(hdc); w != 0 {
			if d, _, _ := odGetDpiForWindow.Call(w); d != 0 {
				return uint32(d)
			}
		}
	}
	od.mu.Lock()
	defer od.mu.Unlock()
	if od.dpi != 0 {
		return od.dpi
	}
	return 96
}

// prepareOwnerDrawnMenu runs before the menu opens: it gives the menu the
// drawer's background colour and has Windows measure every owner-drawn
// item and separator again (the pointer's monitor, its DPI, the menu font
// or the item's content can have changed since the last time). Windows
// keeps an owner-drawn item's size until the item is inserted anew (a
// SetMenuItemInfo of the same type does not ask again, CI run
// 45697510079), so each visible one is removed and inserted again at its
// place.
func prepareOwnerDrawnMenu(menu windows.Handle) {
	od.mu.Lock()
	d := od.drawer
	items := make([]uint32, 0, len(od.items))
	for id := range od.items {
		items = append(items, id)
	}
	seps := make([]uint32, 0, len(od.seps))
	for id := range od.seps {
		seps = append(seps, id)
	}
	od.mu.Unlock()
	if d == nil || menu == 0 {
		return
	}
	if dec, ok := d.(MenuDecorator); ok {
		rgb, ok := dec.MenuBackground()
		setMenuBackground(menu, rgb, ok)
	}
	for _, id := range items {
		menuItemsLock.RLock()
		item := menuItems[id]
		menuItemsLock.RUnlock()
		if item == nil {
			continue
		}
		parent := item.parentId()
		if wt.getVisibleItemIndex(parent, id) == -1 {
			continue // hidden
		}
		if err := wt.hideMenuItem(id, parent); err == nil {
			addOrUpdateMenuItem(item)
		}
	}
	for _, id := range seps {
		if wt.getVisibleItemIndex(0, id) == -1 {
			continue
		}
		if err := wt.hideMenuItem(id, 0); err == nil {
			_ = wt.addSeparatorMenuItem(id, 0)
		}
	}
}

// setMenuBackground fills the menu around its items with rgb (on: true)
// or with the system's colour.
func setMenuBackground(menu windows.Handle, rgb uint32, on bool) {
	const mimBackground = 0x00000002
	od.mu.Lock()
	defer od.mu.Unlock()
	var brush windows.Handle
	if on {
		if od.brush == 0 || od.brushRGB != rgb {
			b, _, _ := odCreateSolidBrush.Call(uintptr(rgb))
			if b == 0 {
				return
			}
			old := od.brush
			od.brush, od.brushRGB = windows.Handle(b), rgb
			defer func() {
				if old != 0 {
					odDeleteObject.Call(uintptr(old)) // after the menu has the new one
				}
			}()
		}
		brush = od.brush
	}
	mi := struct {
		Size, Mask, Style, Max uint32
		Background             windows.Handle
		ContextHelpID          uint32
		MenuData               uintptr
	}{Mask: mimBackground, Background: brush}
	mi.Size = uint32(unsafe.Sizeof(mi))
	pSetMenuInfo.Call(uintptr(menu), uintptr(unsafe.Pointer(&mi)))
}
