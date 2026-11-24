//go:build darwin

package creds

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestKeychainIntegration stores a fake item with security(1) and reads it
// back through ReadKeychain. It runs only when AI_USAGE_KEYCHAIN_TEST=1.
func TestKeychainIntegration(t *testing.T) {
	if os.Getenv("AI_USAGE_KEYCHAIN_TEST") != "1" {
		t.Skip("set AI_USAGE_KEYCHAIN_TEST=1")
	}
	service := fmt.Sprintf("ai-usage-test-%d", os.Getpid())
	user := os.Getenv("USER")
	payload := credsJSON(time.Now().Add(time.Hour))
	out, err := exec.Command("/usr/bin/security", "add-generic-password", "-s", service, "-a", user, "-w", payload).CombinedOutput()
	if err != nil {
		t.Fatalf("add-generic-password: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", user).Run()
	})
	b, err := ReadKeychain(context.Background(), service)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != payload {
		t.Fatal("keychain content differs")
	}
	l, _, err := parseCreds(b)
	if err != nil || !l.HasToken {
		t.Fatalf("parse: %v", err)
	}
}
