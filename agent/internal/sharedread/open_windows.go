package sharedread

import (
	"os"

	"golang.org/x/sys/windows"
)

// open: GENERIC_READ with FILE_SHARE_READ, FILE_SHARE_WRITE and
// FILE_SHARE_DELETE (os.Open shares only read and write), as Codex's own
// readers open auth.json (review I-1).
func open(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
