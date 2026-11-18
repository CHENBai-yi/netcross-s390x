package endian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"
)

// The tests below assert against literal byte sequences rather than only
// round-tripping. A round-trip test passes just as happily on a host that
// writes little-endian as long as it also reads little-endian, which is
// exactly the bug this package exists to prevent. On s390x a round-trip test
// would be actively misleading: the host is big-endian, so an unsafe cast
// implementation would pass here and corrupt every frame on amd64.

func TestPutUint16IsBigEndianOnTheWire(t *testing.T) {
	w := NewWriter(2)
	w.PutUint16(0x0102)
	if err := w.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []byte{0x01, 0x02}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("PutUint16(0x0102) = % x, want % x", w.Bytes(), want)
	}
}

func TestPutUint32IsBigEndianOnTheWire(t *testing.T) {
	w := NewWriter(4)
	w.PutUint32(0xDEADBEEF)
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("PutUint32 = % x, want % x", w.Bytes(), want)
	}
}

func TestPutUint64IsBigEndianOnTheWire(t *testing.T) {
	w := NewWriter(8)
	w.PutUint64(0x0102030405060708)
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("PutUint64 = % x, want % x", w.Bytes(), want)
	}
}

func TestPutFloat64IsBigEndianIEEE754(t *testing.T) {
	w := NewWriter(8)
	w.PutFloat64(1.0)
	// IEEE 754 binary64 for 1.0 is 0x3FF0000000000000.
	want := []byte{0x3F, 0xF0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("PutFloat64(1.0) = % x, want % x", w.Bytes(), want)
	}

	w2 := NewWriter(8)
	w2.PutFloat64(-2.5)
	// -2.5 = sign 1, exponent 0x400, mantissa .25 -> 0xC004000000000000
	want2 := []byte{0xC0, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(w2.Bytes(), want2) {
		t.Fatalf("PutFloat64(-2.5) = % x, want % x", w2.Bytes(), want2)
	}
}

func TestPutFloat32IsBigEndianIEEE754(t *testing.T) {
	w := NewWriter(4)
	w.PutFloat32(1.0)
	// binary32 for 1.0 is 0x3F800000.
	want := []byte{0x3F, 0x80, 0x00, 0x00}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("PutFloat32(1.0) = % x, want % x", w.Bytes(), want)
	}
}

func TestNegativeIntegersUseTwosComplement(t *testing.T) {
	w := NewWriter(16)
	w.PutInt16(-2)
	w.PutInt32(-2)
	w.PutInt64(-2)
	want := []byte{
		0xFF, 0xFE,
		0xFF, 0xFF, 0xFF, 0xFE,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("two's complement encode = % x, want % x", w.Bytes(), want)
	}
}

func TestRoundTripAllScalarTypes(t *testing.T) {
	type sample struct {
		u8  uint8
		i8  int8
		u16 uint16
		i16 int16
		u32 uint32
		i32 int32
		u64 uint64
		i64 int64
		f32 float32
		f64 float64
		b   bool
		d   time.Duration
	}

	cases := []sample{
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, false, 0},
		{255, -128, 65535, -32768, math.MaxUint32, math.MinInt32,
			math.MaxUint64, math.MinInt64, float32(math.Inf(-1)), math.Inf(1), true, -time.Second},
		{42, 127, 0x1234, 1234, 0x89ABCDEF, -123456,
			0x0123456789ABCDEF, 1234567890123, 3.5, -0.125, true, 1500 * time.Microsecond},
		{7, -7, 7, -7, 7, -7, 7, -7, float32(math.NaN()), math.NaN(), false, 7},
	}

	for i, c := range cases {
		w := NewWriter(64)
		w.PutUint8(c.u8)
		w.PutInt8(c.i8)
		w.PutUint16(c.u16)
		w.PutInt16(c.i16)
		w.PutUint32(c.u32)
		w.PutInt32(c.i32)
		w.PutUint64(c.u64)
		w.PutInt64(c.i64)
		w.PutFloat32(c.f32)
		w.PutFloat64(c.f64)
		w.PutBool(c.b)
		w.PutDuration(c.d)
		if err := w.Err(); err != nil {
			t.Fatalf("case %d write: %v", i, err)
		}

		r := NewBuffer(w.Bytes())
		got := sample{
			u8:  r.Uint8(),
			i8:  r.Int8(),
			u16: r.Uint16(),
			i16: r.Int16(),
			u32: r.Uint32(),
			i32: r.Int32(),
			u64: r.Uint64(),
			i64: r.Int64(),
			f32: r.Float32(),
			f64: r.Float64(),
			b:   r.Bool(),
			d:   r.Duration(),
		}
		if err := r.Err(); err != nil {
			t.Fatalf("case %d read: %v", i, err)
		}
		if r.Len() != 0 {
			t.Fatalf("case %d: %d bytes left over", i, r.Len())
		}

		// NaN != NaN, so compare bit patterns for the floats.
		if math.Float32bits(got.f32) != math.Float32bits(c.f32) {
			t.Errorf("case %d f32: got %v want %v", i, got.f32, c.f32)
		}
		if math.Float64bits(got.f64) != math.Float64bits(c.f64) {
			t.Errorf("case %d f64: got %v want %v", i, got.f64, c.f64)
		}
		// Compare everything except the float fields (already checked
		// bitwise above, and NaN never compares equal).
		got.f32, got.f64 = c.f32, c.f64
		if got.u8 != c.u8 || got.i8 != c.i8 || got.u16 != c.u16 || got.i16 != c.i16 ||
			got.u32 != c.u32 || got.i32 != c.i32 || got.u64 != c.u64 || got.i64 != c.i64 ||
			got.b != c.b || got.d != c.d {
			t.Errorf("case %d: round trip mismatch\n got %+v\nwant %+v", i, got, c)
		}
	}
}

func TestShortBufferLatchesError(t *testing.T) {
	r := NewBuffer([]byte{0x01, 0x02})
	if v := r.Uint32(); v != 0 {
		t.Fatalf("partial read should return zero value, got %d", v)
	}
	err := r.Err()
	if !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("want ErrShortBuffer, got %v", err)
	}
	// The error must stay latched and not be overwritten by later calls.
	_ = r.Uint8()
	if !errors.Is(r.Err(), ErrShortBuffer) {
		t.Fatalf("latched error was replaced: %v", r.Err())
	}
}

func TestShortBufferLeavesCursorUntouched(t *testing.T) {
	r := NewBuffer([]byte{0x01, 0x02, 0x03})
	_ = r.Uint16()
	before := r.Offset()
	_ = r.Uint64() // needs 8, only 1 left
	if r.Offset() != before {
		t.Fatalf("failed read moved cursor from %d to %d", before, r.Offset())
	}
}

func TestLengthPrefixedBytes(t *testing.T) {
	w := NewWriter(16)
	w.PutBytes([]byte("abc"))
	want := []byte{0x00, 0x03, 'a', 'b', 'c'}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("PutBytes = % x, want % x", w.Bytes(), want)
	}

	r := NewBuffer(w.Bytes())
	if got := r.ReadBytes(); string(got) != "abc" {
		t.Fatalf("Bytes() = %q, want abc", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLengthPrefixOverrunIsRejected(t *testing.T) {
	// Length prefix claims 100 bytes but only 2 follow. The reader must not
	// allocate 100 bytes and must not silently return a 2-byte slice.
	r := NewBuffer([]byte{0x00, 0x64, 0xAA, 0xBB})
	if got := r.ReadBytes(); got != nil {
		t.Fatalf("expected nil payload on overrun, got % x", got)
	}
	if !errors.Is(r.Err(), ErrShortBuffer) {
		t.Fatalf("want ErrShortBuffer, got %v", r.Err())
	}
}

func TestBytes32RejectsHugeDeclarationBeforeAllocating(t *testing.T) {
	// 0xFFFFFFFF declared with an empty body: must fail on the cap check,
	// not by attempting a 4 GiB allocation.
	r := NewBuffer([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	if got := r.ReadBytes32(); got != nil {
		t.Fatalf("expected nil, got % x", got)
	}
	if !errors.Is(r.Err(), ErrLengthOverflow) {
		t.Fatalf("want ErrLengthOverflow, got %v", r.Err())
	}
}

func TestBytes32RoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5A}, 70000) // > uint16 max
	w := NewWriter(len(payload) + 4)
	w.PutBytes32(payload)
	if err := w.Err(); err != nil {
		t.Fatalf("write: %v", err)
	}
	if binary.BigEndian.Uint32(w.Bytes()[:4]) != uint32(len(payload)) {
		t.Fatalf("prefix = %d, want %d", binary.BigEndian.Uint32(w.Bytes()[:4]), len(payload))
	}
	r := NewBuffer(w.Bytes())
	if got := r.ReadBytes32(); !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: got %d bytes", len(got))
	}
}

func TestPutBytesTooLargeForPrefix(t *testing.T) {
	w := NewWriter(0)
	w.PutBytes(make([]byte, 70000)) // exceeds uint16
	if !errors.Is(w.Err(), ErrLengthTooLarge) {
		t.Fatalf("want ErrLengthTooLarge, got %v", w.Err())
	}
}

func TestStringRoundTripUTF8(t *testing.T) {
	in := "東京 · Zürich · s390x"
	w := NewWriter(64)
	w.PutString(in)
	if err := w.Err(); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The prefix is the byte length, not the rune count.
	declared := binary.BigEndian.Uint16(w.Bytes()[:2])
	if int(declared) != len(in) {
		t.Fatalf("prefix = %d, want byte length %d", declared, len(in))
	}
	r := NewBuffer(w.Bytes())
	if got := r.ReadString(); got != in {
		t.Fatalf("String() = %q, want %q", got, in)
	}
}

func TestFixedBytesPadsAndRejects(t *testing.T) {
	w := NewWriter(8)
	w.PutFixedBytes([]byte{0xAA}, 4)
	if want := []byte{0xAA, 0, 0, 0}; !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("padding = % x, want % x", w.Bytes(), want)
	}

	w2 := NewWriter(8)
	w2.PutFixedBytes([]byte{1, 2, 3, 4, 5}, 4)
	if !errors.Is(w2.Err(), ErrFixedWidth) {
		t.Fatalf("want ErrFixedWidth, got %v", w2.Err())
	}
}

func TestSeekAndSkip(t *testing.T) {
	r := NewBuffer([]byte{1, 2, 3, 4, 5, 6})
	if err := r.Seek(4); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if got := r.Uint16(); got != 0x0506 {
		t.Fatalf("after seek got 0x%04x, want 0x0506", got)
	}
	if err := r.Seek(0); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if got := r.Uint8(); got != 1 {
		t.Fatalf("after rewind got %d, want 1", got)
	}
	if err := r.Skip(2); err != nil {
		t.Fatalf("skip: %v", err)
	}
	if got := r.Uint8(); got != 4 {
		t.Fatalf("after skip got %d, want 4", got)
	}
}

func TestSeekOutOfRange(t *testing.T) {
	r := NewBuffer([]byte{1, 2, 3})
	for _, off := range []int{-1, 4, 1000} {
		if err := r.Seek(off); !errors.Is(err, ErrSeekOutOfRange) {
			t.Fatalf("Seek(%d) = %v, want ErrSeekOutOfRange", off, err)
		}
	}
	if err := r.Skip(-1); !errors.Is(err, ErrSeekOutOfRange) {
		t.Fatalf("Skip(-1) = %v, want ErrSeekOutOfRange", err)
	}
}

func TestViewAliasesWithoutCopy(t *testing.T) {
	src := []byte{1, 2, 3, 4}
	r := NewBuffer(src)
	view, err := r.View(2)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	view[0] = 0xFF
	if src[0] != 0xFF {
		t.Fatal("View should alias the source slice, not copy it")
	}
	if _, err := r.View(10); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("overlong view = %v, want ErrShortBuffer", err)
	}
}

func TestUnreadAndLen(t *testing.T) {
	r := NewBuffer([]byte{1, 2, 3, 4})
	if r.Len() != 4 || r.Cap() != 4 {
		t.Fatalf("len/cap = %d/%d, want 4/4", r.Len(), r.Cap())
	}
	_ = r.Uint16()
	if r.Len() != 2 {
		t.Fatalf("len after read = %d, want 2", r.Len())
	}
	if want := []byte{3, 4}; !bytes.Equal(r.Unread(), want) {
		t.Fatalf("unread = % x, want % x", r.Unread(), want)
	}
	_ = r.Uint16()
	if r.Unread() != nil {
		t.Fatalf("unread at EOF = % x, want nil", r.Unread())
	}
}

func TestResetKeepsAllocation(t *testing.T) {
	w := NewWriter(128)
	w.PutUint64(1)
	before := cap(w.Bytes())
	w.Reset()
	if w.Len() != 0 || w.Offset() != 0 || w.Err() != nil {
		t.Fatal("Reset did not clear state")
	}
	if cap(w.Bytes()) != before {
		t.Logf("capacity changed on reset: %d -> %d", before, cap(w.Bytes()))
	}
}

// --- struct codec -------------------------------------------------------

type handshake struct {
	Magic    uint32
	Version  uint8
	Flags    uint16
	PeerID   string
	Nonce    [4]byte
	Weight   float64
	Deadline time.Duration
}

func (h *handshake) MarshalBE(w *Buffer) error {
	w.PutUint32(h.Magic)
	w.PutUint8(h.Version)
	w.PutUint16(h.Flags)
	w.PutString(h.PeerID)
	w.PutFixedBytes(h.Nonce[:], 4)
	w.PutFloat64(h.Weight)
	w.PutDuration(h.Deadline)
	return w.Err()
}

func (h *handshake) UnmarshalBE(r *Buffer) error {
	h.Magic = r.Uint32()
	h.Version = r.Uint8()
	h.Flags = r.Uint16()
	h.PeerID = r.ReadString()
	copy(h.Nonce[:], r.FixedBytes(4))
	h.Weight = r.Float64()
	h.Deadline = r.Duration()
	return r.Err()
}

func TestMarshalUnmarshalStruct(t *testing.T) {
	in := handshake{
		Magic:    0x4E435253,
		Version:  1,
		Flags:    0x8001,
		PeerID:   "linuxone-9175",
		Nonce:    [4]byte{0xDE, 0xAD, 0xBE, 0xEF},
		Weight:   0.75,
		Deadline: 250 * time.Millisecond,
	}
	data, err := Marshal(&in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The first four bytes are the magic as a big-endian word. This is the
	// assertion that fails on a little-endian writer.
	if want := []byte{0x4E, 0x43, 0x52, 0x53}; !bytes.Equal(data[:4], want) {
		t.Fatalf("magic bytes = % x, want % x (ASCII \"NCRS\")", data[:4], want)
	}
	if data[4] != 0x01 {
		t.Fatalf("version byte = %d, want 1", data[4])
	}
	if want := []byte{0x80, 0x01}; !bytes.Equal(data[5:7], want) {
		t.Fatalf("flags bytes = % x, want % x", data[5:7], want)
	}
	if want := []byte{0x00, 0x0D}; !bytes.Equal(data[7:9], want) {
		t.Fatalf("string length prefix = % x, want % x (13)", data[7:9], want)
	}

	var out handshake
	if err := Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch\n got %+v\nwant %+v", out, in)
	}
}

func TestUnmarshalRejectsTrailingBytes(t *testing.T) {
	in := handshake{Magic: 1, PeerID: "x", Nonce: [4]byte{1, 2, 3, 4}}
	data, err := Marshal(&in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	data = append(data, 0x00)
	var out handshake
	if err := Unmarshal(data, &out); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("want ErrTrailingBytes, got %v", err)
	}
}

func TestUnmarshalRejectsTruncatedInput(t *testing.T) {
	in := handshake{Magic: 1, PeerID: "abcdef", Nonce: [4]byte{1, 2, 3, 4}, Weight: 2}
	data, err := Marshal(&in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, cut := range []int{0, 3, 5, len(data) - 1} {
		var out handshake
		if err := Unmarshal(data[:cut], &out); err == nil {
			t.Fatalf("truncation to %d bytes was accepted", cut)
		}
	}
}

func TestUnmarshalNilTarget(t *testing.T) {
	if err := Unmarshal([]byte{}, nil); !errors.Is(err, ErrNilTarget) {
		t.Fatalf("want ErrNilTarget, got %v", err)
	}
}

func TestMarshalReturnsLatchedFieldError(t *testing.T) {
	h := handshake{PeerID: string(bytes.Repeat([]byte("x"), 70000))}
	if _, err := Marshal(&h); !errors.Is(err, ErrLengthTooLarge) {
		t.Fatalf("want ErrLengthTooLarge, got %v", err)
	}
}

// BenchmarkBufferPutUint64 measures the cost of the explicit big-endian path.
// On s390x the swap is a no-op for the compiler; the number is here so a
// regression in the codec shows up as a step change in the daily report.
func BenchmarkBufferPutUint64(b *testing.B) {
	w := NewWriter(8 * 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Reset()
		for j := 0; j < 1024; j++ {
			w.PutUint64(uint64(j))
		}
	}
}

func BenchmarkBufferGetUint32(b *testing.B) {
	raw := make([]byte, 4*1024)
	binary.BigEndian.PutUint32(raw[0:4], 0x01020304)
	r := NewBuffer(raw)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Seek(0)
		for j := 0; j < 1024; j++ {
			_ = r.Uint32()
		}
	}
}
