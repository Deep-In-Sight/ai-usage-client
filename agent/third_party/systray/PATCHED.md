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
fails unless this folder is upstream plus exactly that patch.

## Changes

Added files: this `PATCHED.md` and `systray_patch_test.go` (tests of the
changes below; Linux and BSD).

Only `systray_unix.go` (Linux and BSD) is changed:

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

Windows and macOS code is untouched. To update the library, copy the new
upstream release over this folder, re-apply the changes, and record the
new diff with `tools/systray-patch.sh --write` (after changing the version
and hash in that script).
