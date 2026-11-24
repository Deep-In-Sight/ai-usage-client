package sharedread

import "strings"

// longPath gives a Windows path that is too long for CreateFile without
// it the \\?\ prefix, as os.Open does for its own opens (review N-2): a
// Codex folder deeper than about 260 characters would otherwise fail to
// open. The prefix turns off Windows' own clean-up of the path, so "."
// and ".." parts and doubled or forward slashes are resolved here first.
// Short, relative and already prefixed paths are kept as they are. It is
// plain string work, so it is tested on every OS.
func longPath(p string) string {
	if len(p) < 248 || strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return p
	}
	q := strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(q, `\\`): // \\server\share\...
		rest, ok := cleanParts(q[2:], 2)
		if !ok {
			return p
		}
		return `\\?\UNC\` + rest
	case len(q) >= 3 && q[1] == ':' && q[2] == '\\' && (q[0]|0x20 >= 'a' && q[0]|0x20 <= 'z'):
		rest, ok := cleanParts(q[3:], 0)
		if !ok {
			return p
		}
		return `\\?\` + q[:3] + rest
	}
	return p
}

// cleanParts drops empty and "." parts of a backslash path and resolves
// ".." parts; the first keep parts (a UNC server and share) are never
// removed. ok is false when ".." would climb above them.
func cleanParts(s string, keep int) (string, bool) {
	var out []string
	for _, part := range strings.Split(s, `\`) {
		switch part {
		case "", ".":
		case "..":
			if len(out) <= keep {
				return "", false
			}
			out = out[:len(out)-1]
		default:
			out = append(out, part)
		}
	}
	if len(out) < keep {
		return "", false
	}
	return strings.Join(out, `\`), true
}
