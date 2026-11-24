package trayhelper

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const staleChildEnv = "AI_USAGE_TEST_STALE_ICON_CHILD"

// staleTestID is the icon id the child uses (no helper uses it).
const staleTestID = 4242

// TestStaleIconChild is the child of TestAnotherProcessRemovesAStaleIcon:
// it adds a notification icon, says its window and waits to be killed.
func TestStaleIconChild(t *testing.T) {
	if os.Getenv(staleChildEnv) == "" {
		t.Skip("child of TestAnotherProcessRemovesAStaleIcon")
	}
	if err := registerWatch(); err != nil {
		fmt.Println("nowindow", err)
		return
	}
	inst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString(watchClass)
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		fmt.Println("nowindow", err)
		return
	}
	icon, _, _ := watchUser32.NewProc("LoadIconW").Call(0, 32512) // IDI_APPLICATION
	if !shellNotifyIcon(nimAdd, &notifyIconDataW{wnd: windows.Handle(hwnd), id: staleTestID, flags: nifIcon, icon: windows.Handle(icon)}) {
		fmt.Println("noadd")
		return
	}
	fmt.Printf("window %d\n", hwnd)
	time.Sleep(30 * time.Second) // killed long before
}

// startStaleChild starts the child and returns it with its icon's window
// (skips the test without a notification area).
func startStaleChild(t *testing.T, mode string) (*exec.Cmd, uint64) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStaleIconChild$", "-test.v")
	cmd.Env = append(os.Environ(), staleChildEnv+"="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "noadd") || strings.HasPrefix(line, "nowindow") {
			t.Skipf("no notification area here: %s", line)
		}
		if w, ok := strings.CutPrefix(line, "window "); ok {
			window, err := strconv.ParseUint(w, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			go func() { // drain
				for sc.Scan() {
				}
			}()
			return cmd, window
		}
	}
	t.Fatal("the child said no window")
	return nil, 0
}

var pShellNotifyIconGetRect = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconGetRect")

// iconThere asks the notification area whether it has the icon
// (Shell_NotifyIconGetRect: S_OK when it does).
func iconThere(window uint64, id uint32) bool {
	type identifier struct {
		size     uint32
		wnd      windows.Handle
		id       uint32
		guidItem windows.GUID
	}
	n := identifier{wnd: windows.Handle(window), id: id}
	n.size = uint32(unsafe.Sizeof(n))
	var rect [4]int32
	r, _, _ := pShellNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&rect)))
	return r == 0
}

// A helper killed after it added its icon leaves the icon; another process
// removes it with the dead helper's window and id (review n-2). Needs a
// taskbar (CI's Windows runners have one). The measurements go into the
// failure message: whether the icon outlives a killed process, and
// whether another process may remove a live or a dead one's icon.
func TestAnotherProcessRemovesAStaleIcon(t *testing.T) {
	live, lw := startStaleChild(t, "1")
	liveThere := iconThere(lw, staleTestID)
	liveRemoved := removeIcon(lw, staleTestID)
	_ = live.Process.Kill()
	_ = live.Wait()

	cmd, window := startStaleChild(t, "1")
	before := iconThere(window, staleTestID)
	if err := cmd.Process.Kill(); err != nil { // TerminateProcess: the icon is not removed
		t.Fatal(err)
	}
	_ = cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	after := iconThere(window, staleTestID)
	removed := removeIcon(window, staleTestID)
	gone := !iconThere(window, staleTestID)
	again := removeIcon(window, staleTestID)
	if !removed || again {
		t.Fatalf("measured: live icon there %v, removed by another process %v; killed child's icon there before %v, after the kill %v, removed by another process %v, gone after %v, removed twice %v",
			liveThere, liveRemoved, before, after, removed, gone, again)
	}
}

// iconWindow finds this process's window of the tray library's class.
func TestIconWindowIsOurs(t *testing.T) {
	if w := iconWindow(); w != 0 {
		t.Fatalf("a tray window %d before any tray was made", w)
	}
	inst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString(systrayClass)
	wc := wndClassEx{wndProc: pDefWindowProcW.Addr(), instance: windows.Handle(inst), className: cls}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		t.Fatal(err)
	}
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer pDestroyWindow.Call(hwnd)
	if w := iconWindow(); w != uint64(hwnd) {
		t.Fatalf("iconWindow %d, the window is %d", w, hwnd)
	}
}
