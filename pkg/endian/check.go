package endian

import (
	"fmt"
	"runtime"
	"unsafe"
)

// HostOrder returns the byte order of the running host, detected with the same
// unsafe probe the original Info implementation used: the lowest byte of the
// in-memory image of an int32 whose value is 0x01020304 is 0x01 on a
// big-endian machine and 0x04 on a little-endian one.
func HostOrder() WireOrder {
	var i int32 = 0x01020304
	u := unsafe.Pointer(&i)
	pb := (*byte)(u)
	b := *pb

	switch b {
	case 0x04:
		return OrderLittleEndian
	case 0x01:
		return OrderBigEndian
	default:
		return OrderUnknown
	}
}

// MatchesHost reports whether o is the byte order of the running host.
func (o WireOrder) MatchesHost() bool {
	return o == HostOrder()
}

// Info returns the runtime architecture and detected byte order (endianness).
func Info() string {
	var order string
	switch HostOrder() {
	case OrderLittleEndian:
		order = "Little-Endian (LE)"
	case OrderBigEndian:
		order = "Big-Endian (BE - IBM s390x native)"
	default:
		order = "Unknown Endian"
	}

	return fmt.Sprintf("Arch: %s/%s | Endianness: %s", runtime.GOOS, runtime.GOARCH, order)
}
