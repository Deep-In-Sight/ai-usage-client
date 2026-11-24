//go:build !linux && !windows && !(darwin && cgo)

package machine

// osSource: no OS machine identifier is read here (a macOS build without
// cgo, other systems); the network card fallback is used.
func osSource() (string, bool) { return "", false }
