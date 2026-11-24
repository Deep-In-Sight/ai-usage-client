//go:build !linux && !windows && !(darwin && cgo)

package machine

// osSource: no OS machine identifier is read here (a macOS build without
// cgo, other systems), so there is no machine key.
func osSource() (string, bool) { return "", false }
