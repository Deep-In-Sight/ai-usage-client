//go:build !linux

package gnomeext

import "embed"

// forms is empty: the extension is embedded in the Linux agent only.
var forms embed.FS
