package machine

import (
	"os"
	"regexp"
	"strings"
)

var machineID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// osSource is systemd's /etc/machine-id, else D-Bus's copy.
func osSource() (string, bool) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(b)); machineID.MatchString(id) {
			return id, true
		}
	}
	return "", false
}
