//go:build darwin && cgo

package machine

/*
#include <unistd.h>
#include <uuid/uuid.h>
#include <time.h>

static int ai_usage_host_uuid(unsigned char *out) {
	struct timespec wait = {5, 0};
	return gethostuuid(out, &wait);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// osSource is the host UUID (gethostuuid, the IOPlatformUUID), as the
// canonical upper-case UUID string.
func osSource() (string, bool) {
	var u [16]byte
	if C.ai_usage_host_uuid((*C.uchar)(unsafe.Pointer(&u[0]))) != 0 {
		return "", false
	}
	if u == [16]byte{} {
		return "", false
	}
	return fmt.Sprintf("%X-%X-%X-%X-%X", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), true
}
