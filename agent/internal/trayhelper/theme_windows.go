package trayhelper

import (
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// appsUseLightTheme is the user's app mode (HKCU\Software\Microsoft\
// Windows\CurrentVersion\Themes\Personalize, AppsUseLightTheme: 0 dark, 1
// light). It is a widely used registry value, not a documented contract:
// it is only read, and an error when it is not there or cannot be read
// keeps the panel light (themeFor).
func appsUseLightTheme() (uint32, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return 0, err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return uint32(v), err
}

// highContrast reports whether a high-contrast theme is on (its colours
// are the system's, which the light panel uses).
func highContrast() bool {
	const spiGetHighContrast, hcfHighContrastOn = 0x0042, 0x00000001
	var hc struct {
		Size, Flags uint32
		Scheme      *uint16
	}
	hc.Size = uint32(unsafe.Sizeof(hc))
	r, _, _ := pwSystemParametersInfoW.Call(spiGetHighContrast, uintptr(hc.Size), uintptr(unsafe.Pointer(&hc)), 0)
	return r != 0 && hc.Flags&hcfHighContrastOn != 0
}
