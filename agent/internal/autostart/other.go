//go:build !linux

package autostart

func forOS(bool, string, Runner) (Method, error) { return nil, ErrUnsupported }
