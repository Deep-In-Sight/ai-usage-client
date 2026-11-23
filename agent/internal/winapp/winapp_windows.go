package winapp

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// KeyPath is the "Installed apps" entry under HKEY_CURRENT_USER.
const KeyPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall\ClaudeUsage`

// Register writes the entry.
func Register(e Entry) error { return registerAt(KeyPath, e) }

// Unregister removes the entry; no entry is not an error.
func Unregister() error { return unregisterAt(KeyPath) }

// Read loads the entry.
func Read() (Entry, bool) { return readAt(KeyPath) }

func registerAt(key string, e Entry) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, v := range map[string]string{
		"DisplayName": e.DisplayName, "DisplayVersion": e.DisplayVersion, "Publisher": e.Publisher,
		"DisplayIcon": e.DisplayIcon, "UninstallString": e.UninstallString, "QuietUninstallString": e.QuietUninstallString,
		"InstallLocation": e.InstallLocation, "InstallDate": e.InstallDate, "URLInfoAbout": e.URLInfoAbout,
	} {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	for name, v := range map[string]uint32{"EstimatedSize": e.EstimatedSizeKB, "NoModify": e.NoModify, "NoRepair": e.NoRepair} {
		if err := k.SetDWordValue(name, v); err != nil {
			return err
		}
	}
	return nil
}

func unregisterAt(key string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, key)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func readAt(key string) (Entry, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		return Entry{}, false
	}
	defer k.Close()
	s := func(name string) string { v, _, _ := k.GetStringValue(name); return v }
	d := func(name string) uint32 { v, _, _ := k.GetIntegerValue(name); return uint32(v) }
	return Entry{
		DisplayName: s("DisplayName"), DisplayVersion: s("DisplayVersion"), Publisher: s("Publisher"),
		DisplayIcon: s("DisplayIcon"), UninstallString: s("UninstallString"), QuietUninstallString: s("QuietUninstallString"),
		InstallLocation: s("InstallLocation"), InstallDate: s("InstallDate"), URLInfoAbout: s("URLInfoAbout"),
		EstimatedSizeKB: d("EstimatedSize"), NoModify: d("NoModify"), NoRepair: d("NoRepair"),
	}, true
}

// HasConsole reports whether standard output is usable (a console, a file
// or a pipe). A GUI executable started from Explorer has none.
func HasConsole() bool {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	return err == nil && h != 0 && h != windows.InvalidHandle
}

var (
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	user32         = windows.NewLazySystemDLL("user32.dll")
	pAttachConsole = kernel32.NewProc("AttachConsole")
	pMessageBox    = user32.NewProc("MessageBoxW")
)

const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS ((DWORD)-1)

// AttachParentConsole writes standard output and error to the console of
// the process that started this one (cmd or PowerShell), when it has one.
func AttachParentConsole() {
	if r, _, _ := pAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
}

const (
	mbOK              = 0x0
	mbIconInformation = 0x40
	mbSetForeground   = 0x10000
)

// Alert shows a message box and waits for OK.
func Alert(title, text string) {
	t, err1 := windows.UTF16PtrFromString(title)
	m, err2 := windows.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		return
	}
	pMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconInformation|mbSetForeground)
}
