package endian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// knownFrame is a hand-assembled ProbeReq frame carrying the payload "hello".
//
//	4e435253            magic  "NCRS"
//	01                  version 1
//	01                  flags: checksum present
//	0001                type: probe_request
//	00000001            sequence 1
//	00000005            payload length 5
//	f696be2f            CRC-32C (Castagnoli) over the 16 header bytes + payload
//	68656c6c6f          "hello"
//
// The checksum was computed independently of this package (bitwise CRC-32C),
// so this vector catches both a wrong polynomial and a wrong covered range —
// a self-consistent Encode/Decode pair would hide either.
var knownFrame = []byte{
	0x4e, 0x43, 0x52, 0x53,
	0x01, 0x01,
	0x00, 0x01,
	0x00, 0x00, 0x00, 0x01,
	0x00, 0x00, 0x00, 0x05,
	0xf6, 0x96, 0xbe, 0x2f,
	'h', 'e', 'l', 'l', 'o',
}

func TestFrameEncodeMatchesKnownBytes(t *testing.T) {
	f := Frame{Type: MsgProbeReq, Sequence: 1, Payload: []byte("hello")}
	got, err := f.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(got, knownFrame) {
		t.Fatalf("encoded frame mismatch\n got % x\nwant % x", got, knownFrame)
	}
}

func TestFrameDecodeKnownBytes(t *testing.T) {
	f, err := DecodeFrame(knownFrame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if f.Version != 1 {
		t.Errorf("version = %d, want 1", f.Version)
	}
	if f.Type != MsgProbeReq {
		t.Errorf("type = %v, want %v", f.Type, MsgProbeReq)
	}
	if f.Sequence != 1 {
		t.Errorf("sequence = %d, want 1", f.Sequence)
	}
	if string(f.Payload) != "hello" {
		t.Errorf("payload = %q, want hello", f.Payload)
	}
	if f.Flags&FlagChecksumPresent == 0 {
		t.Errorf("checksum flag not set: 0x%02x", f.Flags)
	}
}

func TestChecksumValueIsStable(t *testing.T) {
	// Independent of Encode: feed the known header bytes and payload directly.
	hdr := knownFrame[0:16]
	if got := Checksum(hdr, []byte("hello")); got != 0xf696be2f {
		t.Fatalf("Checksum = 0x%08x, want 0xf696be2f", got)
	}
	// A single flipped payload byte must change the checksum.
	if got := Checksum(hdr, []byte("hellp")); got == 0xf696be2f {
		t.Fatal("checksum did not change after payload mutation")
	}
}

func TestFrameChecksumDetectsPayloadCorruption(t *testing.T) {
	corrupt := append([]byte(nil), knownFrame...)
	corrupt[HeaderSize] ^= 0xFF
	if _, err := DecodeFrame(corrupt); !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
}

func TestFrameChecksumDetectsHeaderCorruption(t *testing.T) {
	// Flipping a bit in the type field must be caught too, because the
	// checksum covers the header. A body-only checksum would let a corrupted
	// type through and the peer would dispatch the payload to the wrong
	// handler.
	corrupt := append([]byte(nil), knownFrame...)
	corrupt[7] ^= 0x01
	if _, err := DecodeFrame(corrupt); !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
}

func TestFrameBadMagic(t *testing.T) {
	bad := append([]byte(nil), knownFrame...)
	bad[0] = 'X'
	if _, err := DecodeFrame(bad); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("want ErrBadMagic, got %v", err)
	}
}

func TestFrameBadVersion(t *testing.T) {
	bad := append([]byte(nil), knownFrame...)
	bad[4] = 9
	// Fix the checksum so the version check is what fires, not the CRC.
	binary.BigEndian.PutUint32(bad[16:20], Checksum(bad[0:16], bad[HeaderSize:]))
	if _, err := DecodeFrame(bad); !errors.Is(err, ErrBadVersion) {
		t.Fatalf("want ErrBadVersion, got %v", err)
	}
}

func TestFrameShortHeader(t *testing.T) {
	for _, n := range []int{0, 1, 19} {
		if _, err := DecodeFrame(knownFrame[:n]); !errors.Is(err, ErrShortFrame) {
			t.Fatalf("header truncated to %d: want ErrShortFrame, got %v", n, err)
		}
	}
}

func TestFrameShortPayload(t *testing.T) {
	// Claim 5 bytes of payload but supply 2.
	short := append([]byte(nil), knownFrame[:HeaderSize+2]...)
	if _, err := DecodeFrame(short); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("want ErrShortFrame, got %v", err)
	}
}

func TestFrameDeclaredLengthTooBig(t *testing.T) {
	big := append([]byte(nil), knownFrame...)
	binary.BigEndian.PutUint32(big[12:16], MaxPayload+1)
	if _, err := DecodeFrame(big); !errors.Is(err, ErrFrameTooBig) {
		t.Fatalf("want ErrFrameTooBig, got %v", err)
	}
}

func TestFrameTrailingBytesRejected(t *testing.T) {
	extra := append(append([]byte(nil), knownFrame...), 0x00)
	if _, err := DecodeFrame(extra); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("want ErrTrailingBytes, got %v", err)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	f := Frame{Type: MsgHeartbeat, Sequence: 7}
	data, err := f.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(data) != HeaderSize {
		t.Fatalf("empty frame length = %d, want %d", len(data), HeaderSize)
	}
	back, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if back.Type != MsgHeartbeat || back.Sequence != 7 || len(back.Payload) != 0 {
		t.Fatalf("empty frame round trip mismatch: %+v", back)
	}
}

func TestFrameMaxPayloadGuard(t *testing.T) {
	f := Frame{Type: MsgBenchBatch, Payload: make([]byte, MaxPayload+1)}
	if _, err := f.Encode(); !errors.Is(err, ErrFrameTooBig) {
		t.Fatalf("want ErrFrameTooBig, got %v", err)
	}
}

func TestDecodeFramesStream(t *testing.T) {
	// Two frames back to back, as one TCP read would deliver them.
	var stream []byte
	for i := 0; i < 2; i++ {
		f := Frame{Type: MsgProbeResp, Sequence: uint32(i), Payload: bytes.Repeat([]byte{byte(i)}, i*3)}
		data, err := f.Encode()
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		stream = append(stream, data...)
	}
	frames, err := DecodeFrames(stream)
	if err != nil {
		t.Fatalf("DecodeFrames: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("decoded %d frames, want 2", len(frames))
	}
	for i, f := range frames {
		if f.Sequence != uint32(i) {
			t.Errorf("frame %d sequence = %d", i, f.Sequence)
		}
		if len(f.Payload) != i*3 {
			t.Errorf("frame %d payload = %d bytes, want %d", i, len(f.Payload), i*3)
		}
	}
}

func TestDecodeFramesStopsAtCorruption(t *testing.T) {
	good, err := Frame{Type: MsgControl, Sequence: 1}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	stream := append(append([]byte(nil), good...), 0xDE, 0xAD, 0xBE, 0xEF)
	frames, err := DecodeFrames(stream)
	if err == nil {
		t.Fatal("expected an error on the trailing garbage")
	}
	if len(frames) != 1 {
		t.Fatalf("valid frames before the error = %d, want 1", len(frames))
	}
}

func TestFrameWriteToAndReadFrame(t *testing.T) {
	// Round-trip through an io.Reader/Writer pair, which is how the frame
	// codec is used over a socket.
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		f := Frame{Type: MsgProbeReq, Sequence: uint32(100 + i), Payload: []byte{byte(i)}}
		if _, err := f.WriteTo(&buf); err != nil {
			t.Fatalf("WriteTo %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame %d: %v", i, err)
		}
		if f.Sequence != uint32(100+i) {
			t.Errorf("frame %d sequence = %d, want %d", i, f.Sequence, 100+i)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("read past end = %v, want io.EOF", err)
	}
}

func TestReadFramePartialHeader(t *testing.T) {
	r := bytes.NewReader(knownFrame[:10])
	if _, err := ReadFrame(r); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("want ErrShortFrame, got %v", err)
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	r := bytes.NewReader(knownFrame[:HeaderSize+2])
	if _, err := ReadFrame(r); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("want ErrShortFrame, got %v", err)
	}
}

func TestMessageTypeString(t *testing.T) {
	if got := MsgProbeResp.String(); got != "probe_response" {
		t.Errorf("MsgProbeResp = %q", got)
	}
	if got := MessageType(0xABCD).String(); got != "msg(0xabcd)" {
		t.Errorf("unknown type = %q, want msg(0xabcd)", got)
	}
}

// --- TLV ---------------------------------------------------------------

func TestTLVEncodeMatchesKnownBytes(t *testing.T) {
	recs := []Record{
		{Tag: TagStatus, Value: []byte{0x00, 0xC8}}, // 200
		{Tag: TagNote, Value: []byte("ok")},         // "ok"
	}
	got, err := EncodeTLV(recs)
	if err != nil {
		t.Fatalf("EncodeTLV: %v", err)
	}
	want := []byte{
		0x00, 0x03, 0x00, 0x02, 0x00, 0xC8,
		0x00, 0x04, 0x00, 0x02, 'o', 'k',
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("TLV bytes\n got % x\nwant % x", got, want)
	}
}

func TestTLVRoundTrip(t *testing.T) {
	lat := make([]byte, 8)
	binary.BigEndian.PutUint64(lat, 1234567)
	recs := []Record{
		{Tag: TagTarget, Value: []byte("1.1.1.1:443")},
		{Tag: TagLatencyNs, Value: lat},
		{Tag: TagStatus, Value: []byte{0x00, 0xC8}},
		{Tag: TagNote, Value: nil}, // zero-length value is legal
	}
	payload, err := EncodeTLV(recs)
	if err != nil {
		t.Fatalf("EncodeTLV: %v", err)
	}
	back, err := DecodeTLV(payload)
	if err != nil {
		t.Fatalf("DecodeTLV: %v", err)
	}
	if len(back) != len(recs) {
		t.Fatalf("decoded %d records, want %d", len(back), len(recs))
	}
	for i := range recs {
		if back[i].Tag != recs[i].Tag {
			t.Errorf("record %d tag = 0x%04x, want 0x%04x", i, back[i].Tag, recs[i].Tag)
		}
		if !bytes.Equal(back[i].Value, recs[i].Value) {
			t.Errorf("record %d value = % x, want % x", i, back[i].Value, recs[i].Value)
		}
	}
	if v, ok := Lookup(back, TagTarget); !ok || string(v) != "1.1.1.1:443" {
		t.Fatalf("Lookup(TagTarget) = %q, %v", v, ok)
	}
	if _, ok := Lookup(back, 0xFFFF); ok {
		t.Fatal("Lookup found a tag that is not present")
	}
}

func TestTLVTruncatedHeader(t *testing.T) {
	for _, payload := range [][]byte{{0x00}, {0x00, 0x01}, {0x00, 0x01, 0x00}} {
		if _, err := DecodeTLV(payload); !errors.Is(err, ErrTLVTruncated) {
			t.Fatalf("payload % x: want ErrTLVTruncated, got %v", payload, err)
		}
	}
}

func TestTLVLengthOverrun(t *testing.T) {
	// Tag 1, declared length 10, but only 2 value bytes present.
	payload := []byte{0x00, 0x01, 0x00, 0x0A, 0xAA, 0xBB}
	if _, err := DecodeTLV(payload); !errors.Is(err, ErrTLVTruncated) {
		t.Fatalf("want ErrTLVTruncated, got %v", err)
	}
}

func TestTLVEncodeValueTooLong(t *testing.T) {
	recs := []Record{{Tag: TagNote, Value: make([]byte, 0x10000)}}
	if _, err := EncodeTLV(recs); !errors.Is(err, ErrLengthTooLarge) {
		t.Fatalf("want ErrLengthTooLarge, got %v", err)
	}
}

func TestTLVEmptyPayload(t *testing.T) {
	recs, err := DecodeTLV(nil)
	if err != nil {
		t.Fatalf("DecodeTLV(nil): %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d records from an empty payload", len(recs))
	}
}

func TestRecordString(t *testing.T) {
	lat := make([]byte, 8)
	binary.BigEndian.PutUint64(lat, 42)
	if got := (Record{Tag: TagLatencyNs, Value: lat}).String(); got != "latency_ns=42" {
		t.Errorf("latency record = %q", got)
	}
	if got := (Record{Tag: TagStatus, Value: []byte{0x00, 0xC8}}).String(); got != "status=200" {
		t.Errorf("status record = %q", got)
	}
	if got := (Record{Tag: TagNote, Value: []byte("hi")}).String(); got != "0x0004=hi" {
		t.Errorf("note record = %q", got)
	}
}

// TestFrameCarriesTLVPayload exercises the two layers together: a TLV payload
// inside a checksummed frame, which is the actual on-the-wire shape.
func TestFrameCarriesTLVPayload(t *testing.T) {
	lat := make([]byte, 8)
	binary.BigEndian.PutUint64(lat, 987654321)
	payload, err := EncodeTLV([]Record{
		{Tag: TagTarget, Value: []byte("9.9.9.9:443")},
		{Tag: TagLatencyNs, Value: lat},
	})
	if err != nil {
		t.Fatalf("EncodeTLV: %v", err)
	}
	data, err := Frame{Type: MsgProbeResp, Sequence: 42, Payload: payload}.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	f, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	recs, err := DecodeTLV(f.Payload)
	if err != nil {
		t.Fatalf("DecodeTLV: %v", err)
	}
	v, ok := Lookup(recs, TagLatencyNs)
	if !ok || binary.BigEndian.Uint64(v) != 987654321 {
		t.Fatalf("latency round trip failed: % x", v)
	}
}

func BenchmarkFrameEncode(b *testing.B) {
	payload := bytes.Repeat([]byte{0x42}, 512)
	f := Frame{Type: MsgProbeResp, Sequence: 1, Payload: payload}
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Encode(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFrameDecode(b *testing.B) {
	payload := bytes.Repeat([]byte{0x42}, 512)
	data, err := Frame{Type: MsgProbeResp, Sequence: 1, Payload: payload}.Encode()
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeFrame(data); err != nil {
			b.Fatal(err)
		}
	}
}
