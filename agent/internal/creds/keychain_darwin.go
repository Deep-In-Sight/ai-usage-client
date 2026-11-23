//go:build darwin

package creds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ReadKeychain runs /usr/bin/security to read a generic password item.
var ReadKeychain KeychainFunc = func(ctx context.Context, service string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-w")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, errors.New("keychain: timed out after 20s")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("keychain: %v: %s", err, msg)
	}
	return bytes.TrimSpace(out), nil
}
