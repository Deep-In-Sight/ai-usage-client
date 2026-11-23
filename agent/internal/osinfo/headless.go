package osinfo

// Headless reports that there is no graphical session: XDG_SESSION_TYPE is
// not x11 or wayland and XDG_CURRENT_DESKTOP is empty. DISPLAY is not looked
// at on purpose (SSH X forwarding sets it on servers). This is the one place
// the rule lives.
func Headless(getenv func(string) string) bool {
	switch getenv("XDG_SESSION_TYPE") {
	case "x11", "wayland":
		return false
	}
	return getenv("XDG_CURRENT_DESKTOP") == ""
}
