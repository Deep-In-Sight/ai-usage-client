// Package machine computes the machine key, which tells the server which
// PC a device runs on (design D8), so a reinstall on the same PC is counted
// once. It is a keyed hash (HMAC-SHA256 with a fixed, public message) of
// the OS's own machine identifier: Linux machine-id, Windows MachineGuid,
// macOS host UUID. These are random 128-bit values, so the key cannot be
// turned back into the identifier; but anyone who already has a PC's raw
// identifier can compute its key and so link the two. When the OS gives no
// identifier there is no key (no network card address is ever used: a
// 48-bit address with a public vendor prefix could be found from its
// hash). The identifier never leaves this package: not sent, not stored,
// not logged; the key itself is not logged either.
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

// SourceOS names the only source: the OS's machine identifier.
const SourceOS = "os"

var (
	once           sync.Once
	cachedKey, src string
)

// Key returns the hashed machine key (32 lower-case hex characters) and
// its source ("os"), or ("", "") when the OS gives no identifier. It is
// computed once per process.
func Key() (key, source string) {
	once.Do(func() { cachedKey, src = compute(osSource) })
	return cachedKey, src
}

// compute derives the key from the OS's identifier.
func compute(fromOS func() (string, bool)) (string, string) {
	if raw, ok := fromOS(); ok {
		if k := derive(raw); k != "" {
			return k, SourceOS
		}
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
