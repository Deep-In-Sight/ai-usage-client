//go:build !linux && !darwin && !windows

package autostart

func forOS(Options) (Method, error) { return nil, ErrUnsupported }

func othersOS(Options) []Method { return nil }
