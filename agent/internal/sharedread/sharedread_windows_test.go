package sharedread

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// posixRename renames from over to as Rust's std::fs::rename does on
// current Windows (Codex's FileAuthStorage::save): SetFileInformationByHandle
// with FileRenameInfoEx, FILE_RENAME_FLAG_REPLACE_IF_EXISTS and
// FILE_RENAME_FLAG_POSIX_SEMANTICS. It replaces a file that others hold
// open, if they opened it with FILE_SHARE_DELETE.
func posixRename(from, to string) error {
	p, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	// FILE_RENAME_INFO: Flags (4) + padding (4) + RootDirectory (8) +
	// FileNameLength (4) + FileName at offset 20 (64-bit layout).
	const fileNameOffset = 20
	buf := make([]byte, fileNameOffset+2*len(name)+2)
	*(*uint32)(unsafe.Pointer(&buf[0])) = 0x1 | 0x2 // REPLACE_IF_EXISTS | POSIX_SEMANTICS
	*(*uint32)(unsafe.Pointer(&buf[16])) = uint32(2 * len(name))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[fileNameOffset])), len(name)), name)
	const fileRenameInfoEx = 22
	return windows.SetFileInformationByHandle(h, fileRenameInfoEx, &buf[0], uint32(len(buf)))
}

// Review I-1: while the agent holds auth.json open, Codex saving its
// sign-in (a new file renamed over auth.json with POSIX semantics, as
// std::fs::rename does) must succeed. The control shows the failure a
// plain os.Open causes (it does not share delete).
func TestRenameOverAnOpenFileSucceeds(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the FILE_RENAME_INFO layout here is the 64-bit one")
	}
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(auth, `{"old":1}`)

	// control: os.Open (FILE_SHARE_READ|FILE_SHARE_WRITE only)
	f, err := os.Open(auth)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "tmp1"), `{"new":1}`)
	controlErr := posixRename(filepath.Join(dir, "tmp1"), auth)
	f.Close()
	if controlErr == nil {
		t.Fatal("control: the rename over a file held by os.Open succeeded, so this test cannot tell the difference")
	}
	t.Logf("control: rename over a file held by os.Open: %v", controlErr)

	// ours
	g, err := Open(auth)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "tmp2"), `{"new":2}`)
	if err := posixRename(filepath.Join(dir, "tmp2"), auth); err != nil {
		g.Close()
		t.Fatalf("rename over a file held by sharedread.Open: %v", err)
	}
	// the open handle still reads the old content; the path has the new
	b := make([]byte, 64)
	n, _ := g.Read(b)
	g.Close()
	if string(b[:n]) != `{"old":1}` {
		t.Fatalf("held handle read %q", b[:n])
	}
	now, err := os.ReadFile(auth)
	if err != nil || string(now) != `{"new":2}` {
		t.Fatalf("after: %q %v", now, err)
	}
	// MoveFileEx (os.Rename), for the record: it cannot replace an open
	// file even when the reader shares delete
	h, err := Open(auth)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "tmp3"), `{"new":3}`)
	t.Logf("MoveFileEx over a file held by sharedread.Open: %v", os.Rename(filepath.Join(dir, "tmp3"), auth))
	// and a delete while held
	if err := os.Remove(auth); err != nil {
		t.Fatalf("delete while held: %v", err)
	}
	h.Close()
}
