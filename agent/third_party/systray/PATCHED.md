# fyne.io/systray, patched copy

This folder is a copy of [fyne.io/systray](https://github.com/fyne-io/systray)
**v1.12.2** (Apache License 2.0, see `LICENSE`), used through a `replace`
directive in `agent/go.mod`. The `example/` folder, `Makefile` and
`.gitignore` of the upstream module are left out; every other file is as
upstream ships it, except for the changes below.

The exact difference from upstream is recorded in
`agent/third_party/systray.patch`. CI (`agent/tools/systray-patch.sh`)
downloads v1.12.2 through the Go module proxy, checks its hash against the
checksum database's (`h1:Y8DZxgLHsVQt6rY9Zrkkg+j67S7vv/1F2viOWKPpVeA=`) and
fails unless this folder is upstream plus exactly that patch (compared as
text, binary files included) and every file in it is a plain file (git
mode 100644, nothing executable).

## Licence

Upstream's code in this folder, and the changes made to upstream's files
(listed below), are under the Apache License 2.0 in `LICENSE`. The added
files listed below are not upstream code; they are under the AI Usage
client's MIT licence (`LICENSE` at the repository's root).

## Changes

Added files: this `PATCHED.md`, `systray_patch_test.go` (tests of the
Linux and BSD changes), `trayicon/` (`trayicon_windows.go` and its
test, see Windows below), `ownerdraw.go`, `ownerdraw_windows.go` and
`ownerdraw_other.go` (owner-drawn menu items, see Windows below), and
`panelview.go`, `panelview_darwin.go`, `panelview_darwin.m`,
`panelview.h` and `panelview_other.go` (menu items with a view, see
macOS below). Each added Go, Objective-C and C file starts with a comment
saying it is not upstream code and is under the AI Usage client's MIT
licence.

### `systray_unix.go` (Linux and BSD)

- New function `SetLabel(label, guide string)`.
- The StatusNotifierItem object exports the read-only properties
  `XAyatanaLabel` and `XAyatanaLabelGuide` (`org.kde.StatusNotifierItem`
  interface). `SetLabel` updates them (which also sends
  `PropertiesChanged`) and emits the `XAyatanaNewLabel(label, guide)` signal.
- The item's introspection data (`Introspect` on `/StatusNotifierItem`)
  lists these two properties and the signal: `itemIntrospection()` is
  upstream's generated `IntrospectDataStatusNotifierItem` with the three
  members appended. The generated file itself is unchanged.
- Start-up: `nativeStart` builds the property spec, exports it and stores
  `instance.props` while it holds `instance.lock`, and `createPropSpec`
  no longer takes the lock itself (its caller holds it). Upstream builds
  the spec under the lock but stores `props` later, so a `SetIcon`,
  `SetTitle`, `SetTooltip` or `SetLabel` that ran in between was stored
  but never exported (the tray's first draw runs concurrently with
  start-up), until the next change.
- A closed connection (the session bus was restarted, as GNOME does at
  every logout) no longer panics: `SetIcon` and `SetLabel` set their
  properties through `setItemProp`, which calls `prop.SetMust` and logs
  (`systray error: failed to set <name> prop: …`) instead of panicking
  when the `PropertiesChanged` signal cannot be sent. The value is stored
  either way. Upstream panics there, and the panic ends the whole program.
  (`SetTitle` and `SetTooltip` already used `Properties.Set`, which returns
  the error; they are unchanged.)
- New function `ConnectionLost() <-chan struct{}`: closed when the tray's
  connection ends before `Quit`, or when `nativeStart` cannot connect to
  the bus. The copy does not reconnect; the program starts the tray again
  in a new process.
- `nativeEnd` closes the connection only if start-up got one. Upstream
  calls `Close` on a nil connection when start-up failed, which panics on
  `Quit`.
- The header comment says the file was modified.

Why the label: Ubuntu's AppIndicator extension shows `XAyatanaLabel` as
text next to the tray icon and re-reads it on `XAyatanaNewLabel`.
Upstream's `SetTitle` sets only `Title`, which that extension uses as the
accessible name. Hosts that do not know the Ayatana label (KDE) ignore the
extra properties.

### `systray_windows.go` (Windows)

- `loadIconFrom` loads the icon at the size the notification area draws it:
  `LoadImage(path, IMAGE_ICON, cx, cy, LR_LOADFROMFILE)` with `cx`, `cy`
  from `trayicon.Size()`, the small-icon size (`SM_CXSMICON`) at the
  effective DPI of the primary monitor (`GetDpiForMonitor` +
  `GetSystemMetricsForDpi`, falling back to `GetSystemMetrics`). Upstream
  passes 0, 0 and `LR_DEFAULTSIZE`, which loads the large-icon size
  (`SM_CXICON`, 32 px at 100 %), so the shell scaled every tray icon down
  and an ICO's small entries were never shown. The loaded handles are
  cached per file and size (upstream: per file). The `LoadImage` call and
  the size are in the new public package `trayicon` (public so that the
  AI Usage agent can call `trayicon.Size()` and set its icon again when
  the size changes after a display-scale change), whose test
  (`trayicon_windows_test.go`) loads an ICO with one entry per size and
  checks that the entry of exactly the small-icon size comes back
  unscaled. Upstream's own `systray_windows_test.go` does not compile at
  v1.12.2, so a test inside package `systray` could not run on Windows.
- Menu item icons go through the same function, so they too are loaded
  at the small-icon size (they are drawn at that size anyway).
- New function `SetTrayID(id uint32)`: the notification icon's id (`uID`
  of `NOTIFYICONDATA`) is the package variable `trayID` (default 100, as
  upstream's literal) and can be set before `Run`. The AI Usage agent runs
  one tray process per provider from one executable; Windows keeps the
  user's show/hide setting per program and id, so each needs its own id.
- A first `Shell_NotifyIcon(NIM_ADD)` that fails (right after login the
  taskbar may not be there yet) no longer ends start-up: upstream returns
  the error from `initInstance`, so `onReady` never runs (no menu, no
  updates) while a later `TaskbarCreated` adds a bare icon. Here the
  failure is logged and start-up goes on. New field `winTray.added`
  records whether the notification area has the icon; the
  `TaskbarCreated` handler sets it from its own add, and the new function
  `EnsureIcon() bool` adds the icon when it is not there (an add, or a
  modify when an add that reported failure went through) and reports
  whether it is. The agent's helper calls it every 2 s.
- Owner-drawn menu items (the AI Usage usage panel draws its menu
  itself). The new files hold the API and its state; the upstream file
  only calls them:
  - `ownerdraw.go` (every OS): the types `Rect`, `MenuDrawer`
    (`Measure(id, dpi)`, `Draw(id, hdc, rect, state, dpi)`,
    `SettingChanged(area)`), `MenuDecorator` (`MeasureSeparator`,
    `DrawSeparator`, `MenuBackground`) and `MenuOpener` (`MenuOpening`,
    called on the tray's thread before the menu opens): what a drawer may
    also do; and the method `(*MenuItem).ID()`, the item's menu command
    id.
  - `ownerdraw_windows.go`: `SetMenuDrawer(d)`, `(*MenuItem).
    SetOwnerDrawn(on)` (a hidden item stays hidden), `MenuOwnerWindow()`
    (the hidden window that owns the menu), `AddOwnerDrawnSeparator()` and,
    for tests only, `SetReinsertHookForTests(f)` (f runs between an
    item's removal and its insert in the re-insert before an open) and
    `SetReinsertFaultForTests(f)` (an insert of that re-insert fails
    when f says so); the
    handling of `WM_MEASUREITEM` (the size at the effective DPI of the
    monitor under the pointer, `MonitorFromPoint` +
    `shcore!GetDpiForMonitor`, 96 when unknown; the `POINT` is passed as
    the 64-bit and the 32-bit calling conventions want it) and
    `WM_DRAWITEM` (the DPI of the menu window,
    `GetDpiForWindow(WindowFromDC(hdc))`, else the measured one) for the
    owner-drawn items and separators only (`ODT_MENU`);
    `WM_SETTINGCHANGE`'s text to the drawer; before the menu opens (not
    again while it is shown): the drawer's `MenuOpening`, the drawer's
    background colour (`SetMenuInfo`, `MIM_BACKGROUND`, set under `menuMu`
    on the menu that then opens, as a reset may have replaced the one
    there before; the system's once nothing in the menu is owner-drawn)
    and a fresh measure of every
    visible owner-drawn item and separator (Windows keeps an owner-drawn
    item's size until it is inserted anew, so each is removed and inserted
    again at its place, from the parent and state it was last given, never
    reading the item itself; an insert that fails is logged and tried once
    more, as the item could be Quit); a panic in the drawer is logged
    instead of ending the program.
  - One lock (`menuMu`) for the menu's items: every add, update (a menu
    item's picture too: `(*MenuItem).SetIconFromFilePath`), hide, show,
    separator add, removal and reset (the package's wrappers), the switch
    in `SetOwnerDrawn`, the re-insert before an open, the setting of the
    background and the reading of the menu to show hold it, so
    that the re-insert on the tray's thread and the program's goroutines
    never interleave (a hidden item shown again, an item inserted twice).
    It is never held across a drawer call or anything that waits for the
    tray's thread; the comment at `menuMu` gives the reasoning.
  - `ownerdraw_other.go` (not Windows): the same functions doing nothing
    (`AddOwnerDrawnSeparator` is `AddSeparator`).
  - In `systray_windows.go`: `wndProc` passes `WM_MEASUREITEM`,
    `WM_DRAWITEM` and `WM_SETTINGCHANGE` to `ownerDrawMessage` and runs
    the default procedure for what it does not handle (always for
    `WM_SETTINGCHANGE`, as upstream); `addOrUpdateMenuItem` gives an
    owner-drawn item `MFT_OWNERDRAW`, `ItemData` = its id and no string or
    bitmap (`MIIM_FTYPE|MIIM_ID|MIIM_STATE|MIIM_DATA`; its title is not
    converted), and a text item again once that is switched off;
    `addSeparatorMenuItem` gives an owner-drawn separator
    `MFT_SEPARATOR|MFT_OWNERDRAW`; `showMenu` calls
    `prepareOwnerDrawnMenu` before `TrackPopupMenu`, shows the menu it
    returns (read under `menuMu`: upstream reads `t.menus[0]` without a
    lock while `resetMenu` replaces the map; while the menu is shown
    already, no menu, so the second `TrackPopupMenu` fails as before) and
    tells it when `TrackPopupMenu` returned; `resetMenu` forgets the
    owner-drawn items and separators; the wrappers `addOrUpdateMenuItem`,
    `addSeparator`, `hideMenuItem`, `removeMenuItem` and `resetMenu`, and
    `(*MenuItem).SetIconFromFilePath` (which upstream updates the item
    without them), hold `menuMu`.
- The header comment says the file was modified.

### `systray_darwin.m` (macOS)

Menu items with a view (the AI Usage usage panel draws its headers, rows
and the lines that cannot be clicked in the macOS menu; its actions stay
the menu's own items). The new files hold the API and the view; the
upstream file only calls them:

- `panelview.go` (every OS): the types `ViewCanvas` (the menu's fonts,
  measuring, and the steps a view carries out: a fill with rounded
  corners, a line of text, a premultiplied picture; in pixels at the
  view's scale), `ViewColours` (the system's label, secondary label,
  selection and selected text colours for the view's appearance, in
  sRGB) and `PanelViewer` (`ViewSize`, `DrawView`, `ViewLabel`,
  `MenuOpening`, `MenuClosed`, `ViewFailed`), what the program gives to
  decide what each view shows and where.
- `panelview_darwin.go` (cgo): `SetPanelViewer(v)`,
  `(*MenuItem).SetPanelView(on)` (the item gets a view, sized again and
  redrawn, unless the menu is open: an open menu keeps what it opened
  with; off: a text item again, at the menu's close if it is open),
  `ActivateOnMenuOpen(on)` (for a CI build's test switch only: before the
  menu shows, the app makes itself an accessory app and the active app,
  `setActivationPolicy:`, `activateIgnoringOtherApps:` and, from macOS
  14, `activate`) and, for tests only, `RenderPanelViewPNG(id, width,
  dark, scale)` (one item's view drawn alone into a PNG in the light or
  dark appearance over the window background colour; on the main thread)
  and `RaiseInNextPanelViewText()` (the next text a view draws raises an
  Objective-C exception); the exported callbacks the view calls on the
  main thread, each of which logs a panic of the viewer instead of
  letting it end the program.
- `panelview_darwin.m` and `panelview.h`: the view `AIUPanelView`
  (flipped; as wide as the menu, `NSViewWidthSizable`; draws with the
  viewer's steps, its colours resolved for its effective appearance and
  drawn again when that changes; highlighted only when its item is
  enabled and highlighted; at the window's backing scale), text
  measured and drawn in the menu font and its bold and semi-bold faces
  (`menuFontOfSize:`, `systemFontOfSize:weight:`), pictures through an
  sRGB `CGImage`; a click (`mouseUp:` on an enabled item) cancels the
  menu's tracking and selects the item as `menuHandler:` does; VoiceOver:
  the view is an accessibility element with the menu item role and the
  viewer's label, and its press (`accessibilityPerformPress`) on an
  enabled item is a click. Return and keypad Enter (key codes 36 and 76)
  on a highlighted view item: the menu does not send the item's action
  itself (measured on CI, stages 0 and 3 of the drawn panel). The menu of
  an app that is not active does nothing with Return and hands a view the
  keys it does not use, so the view accepts first responder and takes
  them in `keyDown:` (the arrows and Escape stay the menu's), acting on
  the menu's highlighted item (`highlightedItem`) when that is an enabled
  view item, whichever view the key came to (Space does nothing, as on
  the menu's own items: measured). On macOS 26 the view gets Return in an
  active app's menu too (measured on CI with `ActivateOnMenuOpen`); an
  active app's menu that closes on Return without the action (stage 0,
  a view that did not take the key) is covered as well: the delegate
  remembers the last highlighted item and, when the menu closes with no
  action taken and the closing event is that key (key-down or key-up),
  performs the remembered item's action if it is an enabled view item.
  Escape and every other way of closing do nothing. A view's action (a
  click, Return, a VoiceOver press) is taken only while the menu is open
  and once per open: a second one in the same open (a key repeat, Return
  and a press together) and a press on a view of a closed menu (the
  accessibility tree keeps a closed menu) send nothing, and such a press
  answers that it did nothing. An Objective-C exception while a
  view is measured or drawn is caught in the C functions the Go side
  calls (it never unwinds through Go) and reported to the viewer's
  `ViewFailed` once the call into Go has returned. Only documented AppKit
  calls.
- `panelview_other.go` (not macOS): the same functions doing nothing
  (`RenderPanelViewPNG` returns an error).
- In `systray_darwin.m`: it includes `panelview.h`; `menuHandler:`
  notes that an action came; `menuWillOpen:` tells the view code first
  (the viewer's `MenuOpening`, then every view sized again);
  `menu:willHighlightItem:` (new) passes the item on; `menuDidClose:`
  runs the Return fall-back above, takes away the views taken off while
  the menu was open and tells the viewer (`MenuClosed`) before upstream's
  detach; `show_menu` tells the view code before it attaches and opens
  the menu (the CI test switch above); the new method
  `set_menu_item_panel:` and C function `set_menu_item_panel(menuId, on)`
  give an item its view on the main thread (`runInMainThread`, as every
  other menu change).
- The header comment says the file was modified.

### Everything else

To update the library, copy the new upstream release over this folder,
re-apply the changes, and record the new diff with
`tools/systray-patch.sh --write` (after changing the version and hash in
that script).
