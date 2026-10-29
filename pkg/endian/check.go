package endian

import (
	"fmt"
	"runtime"
	"unsafe"
)

// Info returns the runtime architecture and detected byte order (endianness).
func Info() string {
	var i int32 = 0x01020304
	u := unsafe.Pointer(&i)
	pb := (*byte)(u)
	b := *pb

	var order string
	if b == 0x04 {
		order = "Little-Endian (LE)"
	} else if b == 0x01 {
		order = "Big-Endian (BE - IBM s390x native)"
	} else {
		order = "Unknown Endian"
	}

	return fmt.Sprintf("Arch: %s/%s | Endianness: %s", runtime.GOOS, runtime.GOARCH, order)
}
