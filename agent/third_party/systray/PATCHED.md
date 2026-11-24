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

## Changes

Added files: this `PATCHED.md`, `systray_patch_test.go` (tests of the
Linux and BSD changes) and `internal/trayicon/` (`trayicon_windows.go` and
its test, see Windows below). Each added Go file starts with a comment saying it
is not upstream code.

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
  the size are in the new package `internal/trayicon`, whose test
  (`trayicon_windows_test.go`) loads an ICO with one entry per size and
  checks that the entry of exactly the small-icon size comes back
  unscaled. Upstream's own `systray_windows_test.go` does not compile at
  v1.12.2, so a test inside package `systray` could not run on Windows.
- Menu item icons go through the same function, so they too are loaded
  at the small-icon size (they are drawn at that size anyway).
- The header comment says the file was modified.

### Everything else

The macOS code is untouched. To update the library, copy the new
upstream release over this folder, re-apply the changes, and record the
new diff with `tools/systray-patch.sh --write` (after changing the version
and hash in that script).
