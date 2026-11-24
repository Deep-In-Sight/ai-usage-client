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

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
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

// A helper killed after it added its icon must leave no icon once the
// next helper started (review n-2). Measured on CI's windows-2025 (run
// 45403865840): another process may remove a live process's icon, and the
// shell drops a killed process's icon by itself; older shells kept it
// (until the pointer passed over it), and the next helper's removal is
// for them. Needs a taskbar (CI's Windows runners have one).
func TestAKilledHelpersIconIsGoneAfterTheNextHelperStarts(t *testing.T) {
	// another process removes an icon by its window and id
	live, lw := startStaleChild(t, "1")
	if !iconThere(lw, staleTestID) {
		t.Fatal("the child's icon is not in the notification area")
	}
	if !removeIcon(lw, staleTestID) || iconThere(lw, staleTestID) {
		t.Fatal("another process could not remove the icon")
	}
	_ = live.Process.Kill()
	_ = live.Wait()

	// a killed one: what the next helper does leaves no icon either way
	cmd, window := startStaleChild(t, "1")
	if err := cmd.Process.Kill(); err != nil { // TerminateProcess: the icon is not removed by its owner
		t.Fatal(err)
	}
	_ = cmd.Wait()
	stayed := iconThere(window, staleTestID)
	removeStaleIcons([]trayproto.StaleIcon{{Window: window, UID: staleTestID}})
	if iconThere(window, staleTestID) {
		t.Fatal("the killed helper's icon is still there after the next helper removed it")
	}
	t.Logf("the icon stayed after the kill until removed: %v", stayed)
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
