package version

import (
	"strconv"
	"strings"
)

type semver struct {
	core [3]uint64
	pre  []string // nil: a release
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parse reads [v]MAJOR.MINOR.PATCH[-pre]; build metadata is not accepted.
func parse(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	for i, p := range parts {
		if !digits(p) || len(p) > 1 && p[0] == '0' {
			return semver{}, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return semver{}, false
		}
		v.core[i] = n
	}
	if hasPre {
		ids := strings.Split(pre, ".")
		for _, id := range ids {
			if id == "" {
				return semver{}, false
			}
			for _, c := range id {
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
					return semver{}, false
				}
			}
		}
		v.pre = ids
	}
	return v, true
}

// compareIDs compares pre-release identifiers: numbers by value and below
// text, text by byte order.
func compareIDs(a, b string) int {
	na, nb := digits(a), digits(b)
	switch {
	case na && nb:
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case na:
		return -1
	case nb:
		return 1
	}
	return strings.Compare(a, b)
}

func compare(a, b semver) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			if a.core[i] < b.core[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == nil && b.pre == nil:
		return 0
	case a.pre == nil:
		return 1
	case b.pre == nil:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIDs(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// Newer reports whether candidate is a later version than current under
// semver precedence. Anything unreadable is false, and so is every
// candidate for a development or CI build (current "0.0.0-…").
func Newer(candidate, current string) bool {
	if strings.HasPrefix(current, "0.0.0-") {
		return false
	}
	c, ok1 := parse(candidate)
	v, ok2 := parse(current)
	return ok1 && ok2 && compare(c, v) > 0
}
