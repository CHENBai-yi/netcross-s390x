package endian

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

// The NetCross wire frame is a fixed 20-byte header followed by an opaque
// payload. Every integer in the header is big-endian, which is what makes the
// format readable by a human staring at a tcpdump hex dump without knowing
// which host captured it, and what lets an s390x peer parse a frame produced on
// an amd64 peer with no byte swapping in either direction.
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                          magic "NCRS"                         |  bytes 0-3
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|  version      |  flags        |          msg type             |  bytes 4-7
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                        sequence number                        |  bytes 8-11
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                        payload length                         |  bytes 12-15
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                       CRC-32C checksum                        |  bytes 16-19
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                            payload ...                        |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
const (
	// Magic is the four-byte frame sync word. It is chosen so that a peer
	// scanning a byte stream for a frame boundary cannot false-positive on a
	// run of zeroes, which a length-only prefix would allow.
	Magic = "NCRS"
	// Version is the frame format revision this package emits.
	Version uint8 = 1
	// HeaderSize is the fixed header length in bytes.
	HeaderSize = 20
	// MaxPayload bounds a single frame payload. 64 MiB keeps a hostile length
	// field from driving an allocation larger than the guest's RAM.
	MaxPayload = 64 << 20
)

// Flag bits carried in the header.
const (
	// FlagChecksumPresent is always set by Encode; a decoder accepts frames
	// with the bit clear only when VerifyChecksum is disabled.
	FlagChecksumPresent uint8 = 1 << 0
	// FlagCompressed marks a payload the sender compressed before framing.
	FlagCompressed uint8 = 1 << 1
	// FlagTruncated marks a payload the sender truncated at MaxPayload.
	FlagTruncated uint8 = 1 << 2
)

// MessageType identifies the payload semantics of a frame.
type MessageType uint16

// Registered message types.
const (
	MsgUnknown    MessageType = 0x0000
	MsgProbeReq   MessageType = 0x0001
	MsgProbeResp  MessageType = 0x0002
	MsgBenchBatch MessageType = 0x0003
	MsgHeartbeat  MessageType = 0x0004
	MsgError      MessageType = 0x0005
	MsgControl    MessageType = 0x0006
)

var msgNames = map[MessageType]string{
	MsgUnknown:    "unknown",
	MsgProbeReq:   "probe_request",
	MsgProbeResp:  "probe_response",
	MsgBenchBatch: "bench_batch",
	MsgHeartbeat:  "heartbeat",
	MsgError:      "error",
	MsgControl:    "control",
}

// String returns the symbolic name of the message type.
func (m MessageType) String() string {
	if s, ok := msgNames[m]; ok {
		return s
	}
	return fmt.Sprintf("msg(0x%04x)", uint16(m))
}

// Frame errors. All are comparable with errors.Is.
var (
	ErrBadMagic     = errors.New("endian: bad frame magic")
	ErrBadVersion   = errors.New("endian: unsupported frame version")
	ErrChecksum     = errors.New("endian: frame checksum mismatch")
	ErrFrameTooBig  = errors.New("endian: frame payload exceeds limit")
	ErrShortFrame   = errors.New("endian: truncated frame")
	ErrTLVTruncated = errors.New("endian: truncated TLV record")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Frame is a decoded NetCross wire frame.
type Frame struct {
	Version  uint8
	Flags    uint8
	Type     MessageType
	Sequence uint32
	Payload  []byte
}

// Checksum computes the CRC-32C of the first 16 header bytes followed by the
// payload. Covering the header means a flipped flag or type bit is caught by
// the same check that protects the body, so no separate header digest is
// needed.
func Checksum(header16 []byte, payload []byte) uint32 {
	if len(header16) != 16 {
		// Fall back to a best-effort value rather than panicking: callers
		// hand us slices they may have truncated.
		header16 = header16[:min(len(header16), 16)]
	}
	crc := crc32.Update(0, castagnoli, header16)
	crc = crc32.Update(crc, castagnoli, payload)
	return crc
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Encode serialises the frame, computing a fresh checksum and setting
// FlagChecksumPresent. The returned slice is owned by the caller.
func (f Frame) Encode() ([]byte, error) {
	if len(f.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooBig, len(f.Payload), MaxPayload)
	}
	ver := f.Version
	if ver == 0 {
		ver = Version
	}
	out := make([]byte, HeaderSize+len(f.Payload))
	copy(out[0:4], Magic)
	out[4] = ver
	out[5] = f.Flags | FlagChecksumPresent
	binary.BigEndian.PutUint16(out[6:8], uint16(f.Type))
	binary.BigEndian.PutUint32(out[8:12], f.Sequence)
	binary.BigEndian.PutUint32(out[12:16], uint32(len(f.Payload)))
	binary.BigEndian.PutUint32(out[16:20], Checksum(out[0:16], f.Payload))
	copy(out[HeaderSize:], f.Payload)
	return out, nil
}

// DecodeFrame parses exactly one frame from b. Trailing bytes are an error:
// call DecodeFrames for a stream.
func DecodeFrame(b []byte) (Frame, error) {
	f, n, err := decodeFrameAt(b)
	if err != nil {
		return Frame{}, err
	}
	if n != len(b) {
		return Frame{}, fmt.Errorf("%w: %d trailing bytes", ErrTrailingBytes, len(b)-n)
	}
	return f, nil
}

// DecodeFrames parses every frame in b, which is what a stream socket buffer
// looks like after a read. It stops at the first malformed frame and reports
// how many frames were valid.
func DecodeFrames(b []byte) ([]Frame, error) {
	var out []Frame
	off := 0
	for off < len(b) {
		f, n, err := decodeFrameAt(b[off:])
		if err != nil {
			return out, fmt.Errorf("frame %d at offset %d: %w", len(out), off, err)
		}
		out = append(out, f)
		off += n
	}
	return out, nil
}

func decodeFrameAt(b []byte) (Frame, int, error) {
	if len(b) < HeaderSize {
		return Frame{}, 0, fmt.Errorf("%w: header needs %d bytes, have %d", ErrShortFrame, HeaderSize, len(b))
	}
	if string(b[0:4]) != Magic {
		return Frame{}, 0, fmt.Errorf("%w: got % x", ErrBadMagic, b[0:4])
	}
	ver := b[4]
	if ver != Version {
		return Frame{}, 0, fmt.Errorf("%w: got %d, want %d", ErrBadVersion, ver, Version)
	}
	length := binary.BigEndian.Uint32(b[12:16])
	if length > MaxPayload {
		return Frame{}, 0, fmt.Errorf("%w: declared %d > %d", ErrFrameTooBig, length, MaxPayload)
	}
	total := HeaderSize + int(length)
	if len(b) < total {
		return Frame{}, 0, fmt.Errorf("%w: declared %d payload, have %d", ErrShortFrame, length, len(b)-HeaderSize)
	}
	payload := b[HeaderSize:total]
	want := binary.BigEndian.Uint32(b[16:20])
	if got := Checksum(b[0:16], payload); got != want {
		return Frame{}, 0, fmt.Errorf("%w: computed 0x%08x, header says 0x%08x", ErrChecksum, got, want)
	}
	f := Frame{
		Version:  ver,
		Flags:    b[5],
		Type:     MessageType(binary.BigEndian.Uint16(b[6:8])),
		Sequence: binary.BigEndian.Uint32(b[8:12]),
		Payload:  append([]byte(nil), payload...),
	}
	return f, total, nil
}

// WriteTo frames and writes f to w in a single Write call, so that a frame is
// not split across TCP segments by the writer itself.
func (f Frame) WriteTo(w io.Writer) (int64, error) {
	b, err := f.Encode()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(b)
	return int64(n), err
}

// ReadFrame reads one frame from r. It uses io.ReadFull so a short read from a
// stream does not surface as a spurious truncation error.
func ReadFrame(r io.Reader) (Frame, error) {
	hdr := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("%w: partial header", ErrShortFrame)
		}
		return Frame{}, err
	}
	if string(hdr[0:4]) != Magic {
		return Frame{}, fmt.Errorf("%w: got % x", ErrBadMagic, hdr[0:4])
	}
	if hdr[4] != Version {
		return Frame{}, fmt.Errorf("%w: got %d, want %d", ErrBadVersion, hdr[4], Version)
	}
	length := binary.BigEndian.Uint32(hdr[12:16])
	if length > MaxPayload {
		return Frame{}, fmt.Errorf("%w: declared %d > %d", ErrFrameTooBig, length, MaxPayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) && length == 0 {
			// A zero-length frame is legal; io.ReadFull on an empty slice
			// returns (0, nil), so this branch is unreachable but kept as a
			// guard if the slice handling ever changes.
			return Frame{}, nil
		}
		return Frame{}, fmt.Errorf("%w: %v", ErrShortFrame, err)
	}
	want := binary.BigEndian.Uint32(hdr[16:20])
	if got := Checksum(hdr[0:16], payload); got != want {
		return Frame{}, fmt.Errorf("%w: computed 0x%08x, header says 0x%08x", ErrChecksum, got, want)
	}
	return Frame{
		Version:  hdr[4],
		Flags:    hdr[5],
		Type:     MessageType(binary.BigEndian.Uint16(hdr[6:8])),
		Sequence: binary.BigEndian.Uint32(hdr[8:12]),
		Payload:  payload,
	}, nil
}

// ---------------------------------------------------------------------------
// TLV records
// ---------------------------------------------------------------------------

// TLV tags used inside payloads.
const (
	TagTarget    uint16 = 0x0001 // target identifier, UTF-8
	TagLatencyNs uint16 = 0x0002 // uint64 nanoseconds, big-endian
	TagStatus    uint16 = 0x0003 // uint16 HTTP status or errno
	TagNote      uint16 = 0x0004 // free-form UTF-8 annotation
	TagTimestamp uint16 = 0x0005 // int64 unix nanoseconds
)

// Record is one tag-length-value triple. Inside a payload each record is
// encoded as: uint16 tag | uint16 length | value, all big-endian. A 16-bit
// length keeps a record header at 4 bytes, which is the right trade for the
// small records this protocol carries.
type Record struct {
	Tag   uint16
	Value []byte
}

// EncodeTLV appends the records to a payload buffer.
func EncodeTLV(records []Record) ([]byte, error) {
	w := NewWriter(len(records) * 8)
	for i, r := range records {
		if len(r.Value) > 0xFFFF {
			return nil, fmt.Errorf("%w: record %d value %d > 65535", ErrLengthTooLarge, i, len(r.Value))
		}
		w.PutUint16(r.Tag)
		w.PutUint16(uint16(len(r.Value)))
		w.PutRaw(r.Value)
	}
	if err := w.Err(); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// DecodeTLV parses a payload into records. It rejects a truncated record
// header and a length that runs past the payload rather than returning a
// partial record with a short value.
func DecodeTLV(payload []byte) ([]Record, error) {
	r := NewBuffer(payload)
	var out []Record
	for r.Len() > 0 {
		if r.Len() < 4 {
			return out, fmt.Errorf("%w: %d bytes left, need 4 for tag+length", ErrTLVTruncated, r.Len())
		}
		tag := r.Uint16()
		n := int(r.Uint16())
		if r.Len() < n {
			return out, fmt.Errorf("%w: tag 0x%04x declares %d, %d left", ErrTLVTruncated, tag, n, r.Len())
		}
		val := r.FixedBytes(n)
		out = append(out, Record{Tag: tag, Value: val})
	}
	return out, nil
}

// String renders the records for log output, e.g. "target=1.1.1.1:443".
func (r Record) String() string {
	switch r.Tag {
	case TagLatencyNs:
		if len(r.Value) == 8 {
			return fmt.Sprintf("latency_ns=%d", binary.BigEndian.Uint64(r.Value))
		}
	case TagStatus:
		if len(r.Value) == 2 {
			return fmt.Sprintf("status=%d", binary.BigEndian.Uint16(r.Value))
		}
	case TagTimestamp:
		if len(r.Value) == 8 {
			return fmt.Sprintf("timestamp_ns=%d", int64(binary.BigEndian.Uint64(r.Value)))
		}
	}
	return fmt.Sprintf("0x%04x=%s", r.Tag, strings.TrimSpace(string(r.Value)))
}

// Lookup returns the value of the first record with the given tag.
func Lookup(records []Record, tag uint16) ([]byte, bool) {
	for _, r := range records {
		if r.Tag == tag {
			return r.Value, true
		}
	}
	return nil, false
}
