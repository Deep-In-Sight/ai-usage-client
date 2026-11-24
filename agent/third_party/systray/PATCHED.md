# fyne.io/systray, patched copy

This folder is a copy of [fyne.io/systray](https://github.com/fyne-io/systray)
**v1.12.2** (Apache License 2.0, see `LICENSE`), used through a `replace`
directive in `agent/go.mod`. The `example/` folder, `Makefile` and
`.gitignore` of the upstream module are left out; every other file is as
upstream ships it, except for the change below.

## Change

Only `systray_unix.go` (Linux and BSD) is changed:

- New function `SetLabel(label, guide string)`.
- The StatusNotifierItem object now exports the read-only properties
  `XAyatanaLabel` and `XAyatanaLabelGuide` (`org.kde.StatusNotifierItem`
  interface). `SetLabel` updates them (which also sends
  `PropertiesChanged`) and emits the `XAyatanaNewLabel(label, guide)` signal.

Why: Ubuntu's AppIndicator extension shows `XAyatanaLabel` as text next to
the tray icon and re-reads it on `XAyatanaNewLabel`. Upstream's `SetTitle`
sets only `Title`, which that extension uses as the accessible name. Hosts
that do not know the Ayatana label (KDE) ignore the extra properties.

Windows and macOS code is untouched. To update the library, copy the new
upstream release over this folder and re-apply the change.
