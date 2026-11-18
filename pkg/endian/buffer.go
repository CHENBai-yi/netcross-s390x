package endian

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// Errors returned by the codec. All of them are values the caller can test
// with errors.Is, and none of the operations in this package panic on bad
// input: every failure is reported through the returned error and latched in
// Buffer.Err so that a chain of writes/reads can be checked once at the end.
var (
	// ErrShortBuffer means a read needed more bytes than the buffer holds.
	ErrShortBuffer = errors.New("endian: short buffer")
	// ErrLengthOverflow means a length prefix pointed past the end of the buffer.
	ErrLengthOverflow = errors.New("endian: length prefix exceeds remaining bytes")
	// ErrNegativeLength means a signed length prefix was negative.
	ErrNegativeLength = errors.New("endian: negative length prefix")
	// ErrLengthTooLarge means a value does not fit its declared prefix width.
	ErrLengthTooLarge = errors.New("endian: value exceeds length prefix width")
	// ErrTrailingBytes means a decode consumed fewer bytes than were supplied.
	ErrTrailingBytes = errors.New("endian: trailing bytes after message")
	// ErrSeekOutOfRange means a cursor move landed outside the buffer.
	ErrSeekOutOfRange = errors.New("endian: seek out of range")
	// ErrNilTarget means Unmarshal was handed a nil destination.
	ErrNilTarget = errors.New("endian: nil unmarshal target")
	// ErrFixedWidth means a fixed-width field received the wrong number of bytes.
	ErrFixedWidth = errors.New("endian: fixed-width field size mismatch")
)

// MaxVarintPayload caps how many bytes a single length-prefixed field may
// declare. Without a cap a hostile 4-byte prefix (0xFFFFFFFF) would make a
// decoder attempt a 4 GiB allocation before noticing the frame is short; on a
// 2-core LinuxONE guest that is an OOM kill rather than an error.
const MaxVarintPayload = 16 << 20 // 16 MiB

// Buffer is a cursor-based byte buffer that always encodes multi-byte scalars
// in network byte order (big-endian), independent of the host CPU.
//
// On IBM s390x the host is already big-endian, so a naive
// `*(*uint32)(unsafe.Pointer(&b[0]))` works natively and silently breaks on
// amd64/arm64. Buffer deliberately does not take that shortcut: it routes every
// scalar through encoding/binary.BigEndian so that bytes on the wire are
// identical on all three architectures the project targets, at the cost of a
// byte-swap that is free on s390x and one instruction elsewhere.
//
// A Buffer is either a writer (grown by Put*) or a reader (consumed by Get*);
// both share the same cursor, so a buffer written in place can be rewound with
// Seek(0) and read back. A Buffer is not safe for concurrent use.
type Buffer struct {
	buf []byte
	off int
	err error
}

// NewBuffer wraps an existing slice for reading. The slice is not copied.
func NewBuffer(b []byte) *Buffer {
	return &Buffer{buf: b}
}

// NewWriter returns an empty Buffer with room for capacity bytes.
func NewWriter(capacity int) *Buffer {
	if capacity < 0 {
		capacity = 0
	}
	return &Buffer{buf: make([]byte, 0, capacity)}
}

// Len reports the number of unread bytes (reader view).
func (b *Buffer) Len() int {
	if b.off >= len(b.buf) {
		return 0
	}
	return len(b.buf) - b.off
}

// Cap reports the total size of the underlying slice.
func (b *Buffer) Cap() int { return len(b.buf) }

// Offset reports the current cursor position.
func (b *Buffer) Offset() int { return b.off }

// Err returns the first error latched by any operation on this Buffer.
func (b *Buffer) Err() error { return b.err }

// Reset clears the buffer and rewinds the cursor, keeping the allocation.
func (b *Buffer) Reset() {
	b.buf = b.buf[:0]
	b.off = 0
	b.err = nil
}

// Bytes returns the whole underlying slice, regardless of cursor position.
func (b *Buffer) Bytes() []byte { return b.buf }

// Unread returns the not-yet-consumed tail without copying.
func (b *Buffer) Unread() []byte {
	if b.off >= len(b.buf) {
		return nil
	}
	return b.buf[b.off:]
}

// Seek moves the read cursor to an absolute offset.
func (b *Buffer) Seek(off int) error {
	if off < 0 || off > len(b.buf) {
		return fmt.Errorf("%w: offset %d, size %d", ErrSeekOutOfRange, off, len(b.buf))
	}
	b.off = off
	return nil
}

// Skip advances the cursor by n bytes.
func (b *Buffer) Skip(n int) error {
	if n < 0 {
		return fmt.Errorf("%w: skip %d", ErrSeekOutOfRange, n)
	}
	return b.Seek(b.off + n)
}

func (b *Buffer) setErr(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

// need reserves n bytes for writing and returns the slice to fill.
func (b *Buffer) need(n int) []byte {
	if b.err != nil {
		return nil
	}
	if n < 0 {
		b.setErr(fmt.Errorf("%w: %d", ErrNegativeLength, n))
		return nil
	}
	start := len(b.buf)
	b.buf = append(b.buf, make([]byte, n)...)
	return b.buf[start : start+n]
}

// take consumes n bytes for reading.
func (b *Buffer) take(n int) []byte {
	if b.err != nil {
		return nil
	}
	if n < 0 {
		b.setErr(fmt.Errorf("%w: %d", ErrNegativeLength, n))
		return nil
	}
	if len(b.buf)-b.off < n {
		b.setErr(fmt.Errorf("%w: need %d, have %d at offset %d",
			ErrShortBuffer, n, len(b.buf)-b.off, b.off))
		return nil
	}
	out := b.buf[b.off : b.off+n]
	b.off += n
	return out
}

// ---------------------------------------------------------------------------
// Writers
// ---------------------------------------------------------------------------

// PutUint8 appends a single byte.
func (b *Buffer) PutUint8(v uint8) {
	if p := b.need(1); p != nil {
		p[0] = v
	}
}

// PutBool appends 1 for true and 0 for false.
func (b *Buffer) PutBool(v bool) {
	if p := b.need(1); p != nil {
		if v {
			p[0] = 1
		} else {
			p[0] = 0
		}
	}
}

// PutUint16 appends v in big-endian order.
func (b *Buffer) PutUint16(v uint16) {
	if p := b.need(2); p != nil {
		binary.BigEndian.PutUint16(p, v)
	}
}

// PutUint32 appends v in big-endian order.
func (b *Buffer) PutUint32(v uint32) {
	if p := b.need(4); p != nil {
		binary.BigEndian.PutUint32(p, v)
	}
}

// PutUint64 appends v in big-endian order.
func (b *Buffer) PutUint64(v uint64) {
	if p := b.need(8); p != nil {
		binary.BigEndian.PutUint64(p, v)
	}
}

// PutInt8 appends v reinterpreted as an unsigned byte (two's complement).
func (b *Buffer) PutInt8(v int8) { b.PutUint8(uint8(v)) }

// PutInt16 appends v as a two's-complement big-endian word.
func (b *Buffer) PutInt16(v int16) { b.PutUint16(uint16(v)) }

// PutInt32 appends v as a two's-complement big-endian doubleword.
func (b *Buffer) PutInt32(v int32) { b.PutUint32(uint32(v)) }

// PutInt64 appends v as a two's-complement big-endian quadword.
func (b *Buffer) PutInt64(v int64) { b.PutUint64(uint64(v)) }

// PutFloat32 appends the IEEE 754 binary32 bit pattern of v, big-endian.
func (b *Buffer) PutFloat32(v float32) { b.PutUint32(math.Float32bits(v)) }

// PutFloat64 appends the IEEE 754 binary64 bit pattern of v, big-endian.
func (b *Buffer) PutFloat64(v float64) { b.PutUint64(math.Float64bits(v)) }

// PutDuration appends d as a signed 64-bit nanosecond count.
func (b *Buffer) PutDuration(d time.Duration) { b.PutInt64(int64(d)) }

// PutFixedBytes appends exactly width bytes: src is right-padded with zeroes
// when shorter and rejected when longer. This is the shape used by wire
// headers with reserved padding, where a length prefix would waste bytes.
func (b *Buffer) PutFixedBytes(src []byte, width int) {
	if len(src) > width {
		b.setErr(fmt.Errorf("%w: got %d bytes, field is %d", ErrFixedWidth, len(src), width))
		return
	}
	p := b.need(width)
	if p == nil {
		return
	}
	for i := range p {
		p[i] = 0
	}
	copy(p, src)
}

// PutBytes appends a uint16 length prefix followed by the payload.
func (b *Buffer) PutBytes(v []byte) {
	if len(v) > math.MaxUint16 {
		b.setErr(fmt.Errorf("%w: %d > %d", ErrLengthTooLarge, len(v), math.MaxUint16))
		return
	}
	b.PutUint16(uint16(len(v)))
	b.PutRaw(v)
}

// PutBytes32 appends a uint32 length prefix followed by the payload.
func (b *Buffer) PutBytes32(v []byte) {
	if len(v) > MaxVarintPayload {
		b.setErr(fmt.Errorf("%w: %d > %d", ErrLengthTooLarge, len(v), MaxVarintPayload))
		return
	}
	b.PutUint32(uint32(len(v)))
	b.PutRaw(v)
}

// PutString appends a uint16 length prefix followed by the UTF-8 bytes.
func (b *Buffer) PutString(s string) {
	if len(s) > math.MaxUint16 {
		b.setErr(fmt.Errorf("%w: string %d > %d", ErrLengthTooLarge, len(s), math.MaxUint16))
		return
	}
	b.PutUint16(uint16(len(s)))
	b.PutRaw([]byte(s))
}

// PutRaw appends bytes with no prefix at all.
func (b *Buffer) PutRaw(v []byte) {
	if b.err != nil {
		return
	}
	b.buf = append(b.buf, v...)
}

// ---------------------------------------------------------------------------
// Readers
// ---------------------------------------------------------------------------

// Uint8 reads one byte.
func (b *Buffer) Uint8() uint8 {
	if p := b.take(1); p != nil {
		return p[0]
	}
	return 0
}

// Bool reads one byte and reports whether it was non-zero.
func (b *Buffer) Bool() bool { return b.Uint8() != 0 }

// Uint16 reads a big-endian word.
func (b *Buffer) Uint16() uint16 {
	if p := b.take(2); p != nil {
		return binary.BigEndian.Uint16(p)
	}
	return 0
}

// Uint32 reads a big-endian doubleword.
func (b *Buffer) Uint32() uint32 {
	if p := b.take(4); p != nil {
		return binary.BigEndian.Uint32(p)
	}
	return 0
}

// Uint64 reads a big-endian quadword.
func (b *Buffer) Uint64() uint64 {
	if p := b.take(8); p != nil {
		return binary.BigEndian.Uint64(p)
	}
	return 0
}

// Int8 reads a signed byte.
func (b *Buffer) Int8() int8 { return int8(b.Uint8()) }

// Int16 reads a signed big-endian word.
func (b *Buffer) Int16() int16 { return int16(b.Uint16()) }

// Int32 reads a signed big-endian doubleword.
func (b *Buffer) Int32() int32 { return int32(b.Uint32()) }

// Int64 reads a signed big-endian quadword.
func (b *Buffer) Int64() int64 { return int64(b.Uint64()) }

// Float32 reads an IEEE 754 binary32 value.
func (b *Buffer) Float32() float32 { return math.Float32frombits(b.Uint32()) }

// Float64 reads an IEEE 754 binary64 value.
func (b *Buffer) Float64() float64 { return math.Float64frombits(b.Uint64()) }

// Duration reads a signed 64-bit nanosecond count.
func (b *Buffer) Duration() time.Duration { return time.Duration(b.Int64()) }

// FixedBytes reads exactly width bytes into a fresh slice.
func (b *Buffer) FixedBytes(width int) []byte {
	p := b.take(width)
	if p == nil {
		return nil
	}
	out := make([]byte, width)
	copy(out, p)
	return out
}

// View returns the next n bytes as a subslice aliasing the buffer, advancing
// the cursor. Use it to avoid a copy when the payload is immediately hashed or
// copied into a struct field; do not retain it past the Buffer's lifetime.
func (b *Buffer) View(n int) ([]byte, error) {
	p := b.take(n)
	if p == nil {
		if b.err != nil {
			return nil, b.err
		}
		return nil, ErrShortBuffer
	}
	return p, nil
}

// ReadBytes reads a uint16-prefixed payload into a fresh slice.
func (b *Buffer) ReadBytes() []byte {
	n := int(b.Uint16())
	if b.err != nil {
		return nil
	}
	return b.FixedBytes(n)
}

// ReadBytes32 reads a uint32-prefixed payload into a fresh slice, refusing
// declarations above MaxVarintPayload before allocating.
func (b *Buffer) ReadBytes32() []byte {
	n := int(b.Uint32())
	if b.err != nil {
		return nil
	}
	if n < 0 {
		b.setErr(ErrNegativeLength)
		return nil
	}
	if n > MaxVarintPayload {
		b.setErr(fmt.Errorf("%w: declared %d > cap %d", ErrLengthOverflow, n, MaxVarintPayload))
		return nil
	}
	if n > b.Len() {
		b.setErr(fmt.Errorf("%w: declared %d, remaining %d", ErrLengthOverflow, n, b.Len()))
		return nil
	}
	return b.FixedBytes(n)
}

// ReadString reads a uint16-prefixed UTF-8 string.
func (b *Buffer) ReadString() string {
	p := b.ReadBytes()
	if p == nil {
		return ""
	}
	return string(p)
}

// Raw reads n bytes with no prefix.
func (b *Buffer) Raw(n int) []byte { return b.FixedBytes(n) }

// ---------------------------------------------------------------------------
// Struct codec
// ---------------------------------------------------------------------------

// Marshaler is implemented by types that know how to write themselves into a
// Buffer in big-endian wire order. Fields must be written in a fixed order;
// the matching UnmarshalBE must read them in exactly that order.
type Marshaler interface {
	MarshalBE(w *Buffer) error
}

// Unmarshaler is implemented by types that can populate themselves from a
// Buffer produced by MarshalBE.
type Unmarshaler interface {
	UnmarshalBE(r *Buffer) error
}

// Marshal encodes v into a fresh byte slice. It returns the first latched
// buffer error, so a single failed field aborts the whole encode.
func Marshal(v Marshaler) ([]byte, error) {
	w := NewWriter(64)
	if err := v.MarshalBE(w); err != nil {
		return nil, err
	}
	if err := w.Err(); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// Unmarshal decodes data into v and rejects trailing bytes: a decoder that
// accepts a short prefix of a longer frame hides protocol desynchronisation,
// which on a stream is how two peers silently disagree about record
// boundaries.
func Unmarshal(data []byte, v Unmarshaler) error {
	if v == nil {
		return ErrNilTarget
	}
	r := NewBuffer(data)
	if err := v.UnmarshalBE(r); err != nil {
		return err
	}
	if err := r.Err(); err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("%w: %d bytes left", ErrTrailingBytes, r.Len())
	}
	return nil
}
