// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

// Rect is a rectangle in device pixels (Windows' RECT): the item's place
// in the menu's device context.
type Rect struct{ Left, Top, Right, Bottom int32 }

// MenuDrawer draws the menu items that SetOwnerDrawn made owner-drawn
// (Windows; elsewhere it is never called). Its methods run on the tray's
// own thread while the menu is shown, so they must not block.
type MenuDrawer interface {
	// Measure is the item's size at dpi, the effective DPI of the
	// monitor under the pointer. Windows adds its own gutter to the
	// width: Draw's rectangle can be wider than measured.
	Measure(itemID uint32, dpi uint32) (width, height int32)
	// Draw draws the item into hdc within rc. state has the ODS_* bits
	// (ODS_SELECTED 0x1: under the pointer or the keyboard's choice;
	// ODS_GRAYED 0x2, ODS_DISABLED 0x4, ODS_CHECKED 0x8). dpi is the menu
	// window's, else the one measured.
	Draw(itemID uint32, hdc uintptr, rc Rect, state uint32, dpi uint32)
	// SettingChanged is a WM_SETTINGCHANGE to the tray's window, with
	// its text ("ImmersiveColorSet" when the colour mode changed; "" for
	// none).
	SettingChanged(area string)
}

// MenuDecorator is what a MenuDrawer may also do (Windows): draw the
// separators added with AddOwnerDrawnSeparator and give the menu's
// background colour. A menu that holds an owner-drawn item is drawn in
// the classic style, whose background and separators would otherwise be
// the system's (light) ones.
type MenuDecorator interface {
	MeasureSeparator(dpi uint32) (width, height int32)
	DrawSeparator(hdc uintptr, rc Rect, dpi uint32)
	// MenuBackground is the colour (a COLORREF, 0x00BBGGRR) the menu is
	// filled with around its items, asked each time the menu opens;
	// false: the system's.
	MenuBackground() (rgb uint32, ok bool)
}

// ID is the item's id: on Windows its menu command id, which MenuDrawer's
// calls name.
func (item *MenuItem) ID() uint32 { return item.id }
