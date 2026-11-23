package endian

import (
	"runtime"
	"strings"
	"testing"
)

// canonicalFrame is the big-endian reference frame used by the audit tests: a
// ProbeReq with sequence 0x01020304 and a 7-byte payload, exactly as a
// NetCross peer would emit it.
func canonicalFrame(t *testing.T) []byte {
	t.Helper()
	b, err := Frame{
		Version:  Version,
		Type:     MsgProbeReq,
		Sequence: 0x01020304,
		Payload:  []byte("payload"),
	}.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

// hostOrderTwin byte-swaps every multi-byte header field of b, emulating what a
// little-endian producer writing host order emits. The CRC bytes and the
// payload are left untouched: this models a real corruption, not a frame that
// was re-encoded under a different order.
func hostOrderTwin(b []byte) []byte {
	out := make([]byte, len(b))
	out[0], out[1], out[2], out[3] = b[3], b[2], b[1], b[0]
	out[4] = b[4]
	out[5] = b[5]
	out[6], out[7] = b[7], b[6]
	out[8], out[9], out[10], out[11] = b[11], b[10], b[9], b[8]
	out[12], out[13], out[14], out[15] = b[15], b[14], b[13], b[12]
	copy(out[16:20], b[16:20])
	copy(out[20:], b[20:])
	return out
}

// hasFields reports whether got contains every name in want.
func hasFields(got []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestDetectWireOrderCanonicalIsBigEndian(t *testing.T) {
	b := canonicalFrame(t)
	if got := DetectWireOrder(b); got != OrderBigEndian {
		t.Fatalf("DetectWireOrder = %s, want big-endian", got)
	}

	a := AuditWireOrder(b)
	if !a.MagicOK {
		t.Errorf("MagicOK = false, want true for intact 'NCRS'")
	}
	if a.LengthBE != 7 {
		t.Errorf("LengthBE = %d, want 7", a.LengthBE)
	}
	if a.LengthLE != 0x07000000 {
		t.Errorf("LengthLE = 0x%08x, want 0x07000000", a.LengthLE)
	}
	if !a.LengthPlausible {
		t.Errorf("LengthPlausible = false, want true")
	}
	if len(a.SwappedFields) != 0 {
		t.Errorf("SwappedFields = %v, want empty for a big-endian frame", a.SwappedFields)
	}
}

func TestDetectWireOrderHostOrderTwinIsLittleEndian(t *testing.T) {
	b := hostOrderTwin(canonicalFrame(t))

	// The core test: a naive implementation that only checks b[0:4]=="NCRS"
	// cannot classify this frame and would return unknown.
	if got := DetectWireOrder(b); got != OrderLittleEndian {
		t.Fatalf("DetectWireOrder = %s, want little-endian", got)
	}

	a := AuditWireOrder(b)
	if a.MagicOK {
		t.Errorf("MagicOK = true, want false (magic is 'SRCN')")
	}
	if a.LengthLE != 7 {
		t.Errorf("LengthLE = %d, want 7", a.LengthLE)
	}
	if a.LengthBE != 0x07000000 {
		t.Errorf("LengthBE = 0x%08x, want 0x07000000", a.LengthBE)
	}
	if !hasFields(a.SwappedFields, "sequence", "payload_length", "message_type") {
		t.Errorf("SwappedFields = %v, want it to list sequence, payload_length and message_type", a.SwappedFields)
	}
}

func TestAuditWireOrderShortInput(t *testing.T) {
	a := AuditWireOrder(make([]byte, 10))
	if a.Order != OrderUnknown {
		t.Errorf("Order = %s, want unknown", a.Order)
	}
	if a.LengthPlausible {
		t.Errorf("LengthPlausible = true, want false")
	}
	if a.Reason == "" {
		t.Errorf("Reason is empty, want an explanation")
	}
}

func TestAuditWireOrderNilInput(t *testing.T) {
	a := AuditWireOrder(nil)
	if a.Order != OrderUnknown {
		t.Errorf("Order = %s, want unknown", a.Order)
	}
	if a.Reason == "" {
		t.Errorf("Reason is empty, want an explanation")
	}
}

func TestDetectWireOrderLengthWitnessDetectsLittleEndian(t *testing.T) {
	b := canonicalFrame(t)
	// Leave the magic intact but swap only the payload-length field (bytes
	// 12-15). This is the second-witness path: an implementation that stops at
	// the magic check reports big-endian and misses the byte-swapped length.
	b[12], b[15] = b[15], b[12]
	b[13], b[14] = b[14], b[13]

	got := DetectWireOrder(b)
	if got != OrderLittleEndian {
		t.Fatalf("DetectWireOrder = %s, want little-endian", got)
	}
	if got == OrderBigEndian {
		t.Fatalf("DetectWireOrder returned big-endian for a byte-swapped length field")
	}

	a := AuditWireOrder(b)
	if !a.MagicOK {
		t.Errorf("MagicOK = false, want true (magic was left intact)")
	}
	if a.LengthBE != 0x07000000 {
		t.Errorf("LengthBE = 0x%08x, want 0x07000000", a.LengthBE)
	}
	if a.LengthLE != 7 {
		t.Errorf("LengthLE = %d, want 7", a.LengthLE)
	}
}

func TestHostOrderMatchesRuntime(t *testing.T) {
	got := HostOrder()
	if !got.MatchesHost() {
		t.Fatalf("HostOrder() = %s but MatchesHost() is false", got)
	}

	switch runtime.GOARCH {
	case "s390x":
		if got != OrderBigEndian {
			t.Errorf("HostOrder() = %s on s390x, want big-endian", got)
		}
	case "amd64", "arm64":
		if got != OrderLittleEndian {
			t.Errorf("HostOrder() = %s on %s, want little-endian", got, runtime.GOARCH)
		}
	default:
		t.Skipf("runtime.GOARCH = %s: no known byte-order expectation, skipping", runtime.GOARCH)
	}
}

func TestInfoRegressionReportsHostOrder(t *testing.T) {
	info := Info()
	want := "Little-Endian"
	if HostOrder() == OrderBigEndian {
		want = "Big-Endian"
	}
	if !strings.Contains(info, want) {
		t.Fatalf("Info() = %q, want it to contain %q", info, want)
	}
}

func TestHint(t *testing.T) {
	le := OrderAudit{Order: OrderLittleEndian}
	if h := le.Hint(); !strings.Contains(h, "host byte order") {
		t.Errorf("little-endian Hint() = %q, want it to mention host byte order", h)
	}

	be := OrderAudit{Order: OrderBigEndian}
	if h := be.Hint(); !strings.Contains(h, "as expected") {
		t.Errorf("big-endian Hint() = %q, want an 'as expected' style sentence", h)
	}
}

func TestWireOrderString(t *testing.T) {
	cases := []struct {
		order WireOrder
		want  string
	}{
		{OrderUnknown, "unknown"},
		{OrderBigEndian, "big-endian"},
		{OrderLittleEndian, "little-endian"},
	}
	for _, c := range cases {
		if got := c.order.String(); got != c.want {
			t.Errorf("%d.String() = %q, want %q", c.order, got, c.want)
		}
	}
}

func TestDetectWireOrderShortInputIsUnknown(t *testing.T) {
	if got := DetectWireOrder(make([]byte, 4)); got != OrderUnknown {
		t.Errorf("DetectWireOrder on a 4-byte input = %s, want unknown", got)
	}
}
