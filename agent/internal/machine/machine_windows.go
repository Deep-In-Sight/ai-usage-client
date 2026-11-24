package machine

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// osSource is HKLM\SOFTWARE\Microsoft\Cryptography MachineGuid (the 64-bit
// view, readable by every user), read once at start.
func osSource() (string, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}
