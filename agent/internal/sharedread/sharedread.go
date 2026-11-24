// Package sharedread opens other programs' files (the Claude Code and
// Codex sign-ins and settings) for reading only, in a way that never gets
// in the way of their owner and never holds up the agent:
//   - on Windows the file stays open for others to write, replace by
//     rename and delete (Codex saves auth.json by renaming a new file over
//     it; a reader without FILE_SHARE_DELETE makes that rename fail, and a
//     sign-in renewed in that moment would be lost; review I-1);
//   - only a regular file is read (a FIFO would block; on Unix it is
//     opened non-blocking and refused), and a read is bounded in time, so
//     that a hung network share holds up one sign-in, not the agent
//     (review m-3).
package sharedread

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

var (
	// ErrNotRegular: the path is not a regular file (a FIFO, a device, a
	// folder); it is not read.
	ErrNotRegular = errors.New("not a regular file")
	// ErrTimeout: the read did not end within the time limit, or an
	// earlier read of the same file still has not.
	ErrTimeout = errors.New("the file could not be read in time")
)

// Timeout bounds one open and read.
const Timeout = 5 * time.Second

// Open opens path for reading only, sharing it fully with other programs,
// and refuses anything that is not a regular file. It is not bounded in
// time: use Do or ReadFile for that.
func Open(path string) (*os.File, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return f, nil
}

var (
	busyMu sync.Mutex
	busy   = map[string]bool{}
)

// Do opens path with Open and calls fn with it, within Timeout. When that
// is exceeded Do returns ErrTimeout and leaves the read to end by itself
// (the file is closed then); until it has, Do and ReadFile on that path
// return ErrTimeout at once, so hung reads never pile up.
func Do(path string, fn func(*os.File) error) error { return do(path, Timeout, fn) }

func do(path string, limit time.Duration, fn func(*os.File) error) error {
	busyMu.Lock()
	if busy[path] {
		busyMu.Unlock()
		return &os.PathError{Op: "read", Path: path, Err: ErrTimeout}
	}
	busy[path] = true
	busyMu.Unlock()
	done := make(chan error, 1)
	go func() {
		defer func() {
			busyMu.Lock()
			delete(busy, path)
			busyMu.Unlock()
		}()
		f, err := Open(path)
		if err == nil {
			err = fn(f)
			f.Close()
		}
		done <- err
	}()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		return &os.PathError{Op: "read", Path: path, Err: ErrTimeout}
	}
}

// ReadFile reads at most limit bytes of path (Do), and returns them with
// the file's information as of that read (size and time of the content
// read, not of whatever is on disk later).
func ReadFile(path string, limit int64) ([]byte, os.FileInfo, error) {
	return readFile(path, limit, Timeout)
}

func readFile(path string, limit int64, d time.Duration) ([]byte, os.FileInfo, error) {
	var b []byte
	var info os.FileInfo
	err := do(path, d, func(f *os.File) error {
		var err error
		if info, err = f.Stat(); err != nil {
			return err
		}
		b, err = io.ReadAll(io.LimitReader(f, limit))
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return b, info, nil
}
