//go:build !windows

package sharedread

import (
	"os"
	"syscall"
)

// open: non-blocking, so that opening a FIFO returns at once (Open then
// refuses it); reads of a regular file are not affected.
func open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
