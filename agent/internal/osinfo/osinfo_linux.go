package osinfo

import (
	"os"

	"golang.org/x/sys/unix"
)

func osVersion() string {
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if b, err := os.ReadFile(p); err == nil {
			if v := ParseOSRelease(b); v != "" {
				return v
			}
		}
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		return unix.ByteSliceToString(u.Release[:])
	}
	return ""
}
