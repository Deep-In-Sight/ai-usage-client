package gnomeext

import "embed"

// forms are the extension's generated forms (tools/mkext), embedded in
// the Linux agent only.
//
//go:embed all:gnome42 all:gnome46
var forms embed.FS
