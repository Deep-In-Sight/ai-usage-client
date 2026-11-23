//go:build !windows

package winapp

// Register does nothing outside Windows.
func Register(Entry) error { return nil }

// Unregister does nothing outside Windows.
func Unregister() error { return nil }

// Read finds nothing outside Windows.
func Read() (Entry, bool) { return Entry{}, false }

// HasConsole is always true outside Windows.
func HasConsole() bool { return true }

// AttachParentConsole does nothing outside Windows.
func AttachParentConsole() {}

// Alert does nothing outside Windows.
func Alert(title, text string) {}
