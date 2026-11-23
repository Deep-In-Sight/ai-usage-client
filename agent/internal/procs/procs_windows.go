package procs

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// longPath expands 8.3 short names (C:\Users\RUNNER~1\...) so that it can be
// compared with what QueryFullProcessImageName returns.
func longPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

func imagePath(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf[:n]), true
}

// FindByExe lists the processes it may query whose image is exe (compared
// without regard to case).
func FindByExe(exe string) ([]int, error) {
	want := longPath(exe)
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var pids []int
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == 0 {
			continue
		}
		p, ok := imagePath(e.ProcessID)
		if ok && strings.EqualFold(filepath.Clean(p), want) {
			pids = append(pids, int(e.ProcessID))
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return pids, err
	}
	return pids, nil
}

// Kill ends the given processes except one (usually the caller) and waits
// up to 5 s for them to go.
func Kill(pids []int, except int) {
	var hs []windows.Handle
	for _, pid := range pids {
		if pid == except || pid <= 0 {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			continue
		}
		if windows.TerminateProcess(h, 1) != nil {
			windows.CloseHandle(h)
			continue
		}
		hs = append(hs, h)
	}
	deadline := time.Now().Add(killWait * time.Second)
	for _, h := range hs {
		if left := time.Until(deadline); left > 0 {
			_, _ = windows.WaitForSingleObject(h, uint32(left/time.Millisecond))
		}
		windows.CloseHandle(h)
	}
}

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// StartDetached starts exe with no console, no window and no inherited
// handles.
func StartDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:    detachedProcess | createNewProcessGroup,
		HideWindow:       true,
		NoInheritHandles: true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
