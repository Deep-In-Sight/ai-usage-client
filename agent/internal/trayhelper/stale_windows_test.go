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
	if os.Getenv(staleChildEnv) != "1" {
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

// A helper killed after it added its icon leaves the icon; another process
// removes it with the dead helper's window and id (review n-2). Needs a
// taskbar (CI's Windows runners have one).
func TestAnotherProcessRemovesAStaleIcon(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStaleIconChild$", "-test.v")
	cmd.Env = append(os.Environ(), staleChildEnv+"=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var window uint64
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "noadd") || strings.HasPrefix(line, "nowindow") {
			t.Skipf("no notification area here: %s", line)
		}
		if w, ok := strings.CutPrefix(line, "window "); ok {
			window, err = strconv.ParseUint(w, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if window == 0 {
		t.Fatal("the child said no window")
	}
	if err := cmd.Process.Kill(); err != nil { // TerminateProcess: the icon is not removed
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if !removeIcon(window, staleTestID) {
		t.Fatal("the icon the killed process left could not be removed by another process")
	}
	if removeIcon(window, staleTestID) {
		t.Fatal("removed twice: the first removal did not take it away")
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
