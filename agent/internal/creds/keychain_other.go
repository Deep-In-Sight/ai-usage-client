//go:build !darwin

package creds

import "context"

// ReadKeychain is not available outside macOS.
var ReadKeychain KeychainFunc = func(context.Context, string) ([]byte, error) { return nil, ErrNoKeychain }
