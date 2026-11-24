package sharedread

import (
	"strings"
	"testing"
)

// Review N-2: a long Windows path gets the \\?\ prefix CreateFile needs,
// cleaned the way Windows would have cleaned it without the prefix.
func TestLongPath(t *testing.T) {
	long := strings.Repeat("d", 120)
	for _, c := range []struct{ in, want string }{
		{`C:\Users\a\.codex\auth.json`, `C:\Users\a\.codex\auth.json`}, // short: as it is
		{`C:\` + long + `\` + long + `\auth.json`, `\\?\C:\` + long + `\` + long + `\auth.json`},
		{`C:/` + long + `//` + long + `/./x/../auth.json`, `\\?\C:\` + long + `\` + long + `\auth.json`},
		{`\\server\share\` + long + `\` + long + `\auth.json`, `\\?\UNC\server\share\` + long + `\` + long + `\auth.json`},
		{`\\?\C:\` + long + `\` + long, `\\?\C:\` + long + `\` + long},     // already prefixed
		{`relative\` + long + `\` + long, `relative\` + long + `\` + long}, // not absolute
		{`C:\..\` + long + `\` + long, `C:\..\` + long + `\` + long},       // climbs above the root: left to Windows
	} {
		if got := longPath(c.in); got != c.want {
			t.Errorf("longPath(%.40q…) = %.60q…, want %.60q…", c.in, got, c.want)
		}
	}
}
