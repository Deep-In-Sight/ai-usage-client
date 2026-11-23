package notify

import (
	"runtime"
	"testing"
	"unsafe"
)

// TestNotifyIconDataLayout pins NOTIFYICONDATAW on x64: uTimeout and
// uVersion are one union, so szInfoTitle is at 820 and the struct is 976.
func TestNotifyIconDataLayout(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("offsets checked for x64")
	}
	var n notifyIconData
	if off := unsafe.Offsetof(n.InfoTitle); off != 820 {
		t.Fatalf("szInfoTitle at %d, want 820", off)
	}
	if off := unsafe.Offsetof(n.InfoFlags); off != 948 {
		t.Fatalf("dwInfoFlags at %d, want 948", off)
	}
	if size := unsafe.Sizeof(n); size != 976 {
		t.Fatalf("size %d, want 976", size)
	}
}
