// Package osinfo reports the OS name and version, host name and OS user,
// without running any external command.
package osinfo

import (
	"bufio"
	"bytes"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// Info describes this device.
type Info struct {
	OS, OSVersion, Arch, Hostname, OSUser string
}

// Current reads the values for this process.
func Current() Info {
	i := Info{OS: osName(), OSVersion: osVersion(), Arch: runtime.GOARCH}
	i.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil && u.Username != "" {
		i.OSUser = u.Username
	} else if v := os.Getenv("USER"); v != "" {
		i.OSUser = v
	} else if v := os.Getenv("USERNAME"); v != "" {
		i.OSUser = v
	} else if uid := os.Getuid(); uid >= 0 {
		i.OSUser = strconv.Itoa(uid) // no passwd entry (e.g. in a container): the numeric id
	}
	return i
}

func osName() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

// ParseOSRelease returns VERSION_ID from an /etc/os-release file, or "".
func ParseOSRelease(b []byte) string {
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		v, ok := strings.CutPrefix(line, "VERSION_ID=")
		if !ok {
			continue
		}
		return strings.Trim(v, `"'`)
	}
	return ""
}
