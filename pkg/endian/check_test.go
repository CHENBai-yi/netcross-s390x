package endian

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestInfo(t *testing.T) {
	info := Info()
	if len(info) == 0 {
		t.Fatal("Info() returned empty string")
	}
	t.Logf("Endian info: %s", info)
}

func TestFloat64Serialization(t *testing.T) {
	val := 3.141592653589793
	bits := math.Float64bits(val)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, bits)

	recoveredBits := binary.BigEndian.Uint64(buf)
	recoveredVal := math.Float64frombits(recoveredBits)

	if val != recoveredVal {
		t.Fatalf("Float64 serialization mismatch: got %v, want %v", recoveredVal, val)
	}
}

func BenchmarkEndianCheck(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Info()
	}
}
