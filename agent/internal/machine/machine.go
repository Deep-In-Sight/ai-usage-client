// Package machine computes the machine key: a salted hash that tells the
// server which PC a device runs on (design D8), so a reinstall on the same
// PC is counted once. The source is the OS's own machine identifier
// (Linux machine-id, Windows MachineGuid, macOS host UUID), or the
// address of a real network card when the OS gives none. The source value
// never leaves this package: not sent, not stored, not logged; the key
// itself is not logged either.
package machine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// message is the HMAC message; the source value is the key, as systemd's
// app-specific machine id does it.
const message = "ai-usage machine key v1"

// Source names.
const (
	SourceOS  = "os"
	SourceMAC = "mac"
)

var (
	once           sync.Once
	cachedKey, src string
)

// Key returns the hashed machine key (32 lower-case hex characters) and
// its source ("os" or "mac"), or ("", "") when neither is available. It is
// computed once per process.
func Key() (key, source string) {
	once.Do(func() { cachedKey, src = compute(osSource, interfaces) })
	return cachedKey, src
}

// compute derives the key from the OS source, else from a network card.
func compute(fromOS func() (string, bool), ifs func() []iface) (string, string) {
	if raw, ok := fromOS(); ok {
		if k := derive(raw); k != "" {
			return k, SourceOS
		}
	}
	if raw := pickMAC(ifs()); raw != "" {
		return derive(raw), SourceMAC
	}
	return "", ""
}

// derive is hex(HMAC-SHA256(key = the source in lower case without
// surrounding spaces, message))[:32]. An empty source gives "".
func derive(raw string) string {
	k := strings.ToLower(strings.TrimSpace(raw))
	if k == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(k))
	m.Write([]byte(message))
	return hex.EncodeToString(m.Sum(nil))[:32]
}
