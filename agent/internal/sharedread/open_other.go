//go:build !windows

package sharedread

import "os"

func open(path string) (*os.File, error) { return os.Open(path) }
