// Package sharedread opens other programs' files (the Claude Code and
// Codex sign-ins and settings) for reading only, in a way that never gets
// in the way of their owner: on Windows the file stays open for others to
// write, replace by rename and delete (Codex saves auth.json by renaming
// a new file over it; a reader without FILE_SHARE_DELETE makes that
// rename fail, and a sign-in renewed in that moment would be lost).
package sharedread

import (
	"io"
	"os"
)

// Open opens path for reading only, sharing it fully with other programs.
func Open(path string) (*os.File, error) { return open(path) }

// ReadFile reads at most limit bytes of path, opened with Open.
func ReadFile(path string, limit int64) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
