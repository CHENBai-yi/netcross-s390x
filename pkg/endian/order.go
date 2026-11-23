package endian

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// reversedMagic is the frame sync word with its four bytes in reverse order. A
// producer that writes the magic as a host-order uint32 on a little-endian
// machine emits "SRCN" instead of "NCRS", so seeing it is the fingerprint of a
// host-order (little-endian) serialiser.
const reversedMagic = "SRCN"

// WireOrder is the byte order the multi-byte fields of a candidate wire frame
// were serialised in. It is the diagnosis this package reports when a frame
// fails to decode as canonical big-endian.
type WireOrder int

const (
	// OrderUnknown means no reading of the frame's fields is plausible.
	OrderUnknown WireOrder = iota
	// OrderBigEndian matches the wire format's canonical byte order.
	OrderBigEndian
	// OrderLittleEndian means the frame's fields were serialised by a
	// little-endian (host-order) producer.
	OrderLittleEndian
)

// String renders the order for logs and CLI output.
func (o WireOrder) String() string {
	switch o {
	case OrderBigEndian:
		return "big-endian"
	case OrderLittleEndian:
		return "little-endian"
	default:
		return "unknown"
	}
}

// DetectWireOrder classifies a candidate frame by testing the magic word and the
// payload-length field under both byte-order interpretations.
//
// The magic word is the first witness: an intact "NCRS" says the frame is
// big-endian, while "SRCN" (the byte-reversed sync word a host-order uint32
// write produces on a little-endian producer) says it is little-endian. The
// length field is the second, independent witness: a truncated or partial
// buffer can leave the magic intact while its length field is byte-swapped, so
// if the big-endian length read exceeds MaxPayload while the little-endian read
// lands inside it, the length field is the more reliable signal. DetectWireOrder
// returns OrderUnknown when neither reading is plausible. It never panics on
// short input.
func DetectWireOrder(b []byte) WireOrder {
	if len(b) < HeaderSize {
		return OrderUnknown
	}
	magic := string(b[0:4])
	lenBE := binary.BigEndian.Uint32(b[12:16])
	lenLE := binary.LittleEndian.Uint32(b[12:16])
	bePlausible := lenBE <= MaxPayload
	lePlausible := lenLE <= MaxPayload

	switch {
	case magic == Magic:
		// The sync word is intact. Trust the length field to break the tie
		// when only one reading lands within MaxPayload.
		if bePlausible {
			return OrderBigEndian
		}
		if lePlausible {
			return OrderLittleEndian
		}
	case magic == reversedMagic:
		if lePlausible {
			return OrderLittleEndian
		}
		if bePlausible {
			return OrderBigEndian
		}
	}
	return OrderUnknown
}

// OrderAudit is the result of auditing a candidate frame for foreign byte
// order. It is the diagnosis an operator gets instead of a bare ErrBadMagic,
// so a peer that serialised with host byte order is not misread as packet loss.
type OrderAudit struct {
	// Order is the detected byte order of the frame's header fields.
	Order WireOrder
	// MagicOK reports whether the sync word is the canonical "NCRS".
	MagicOK bool
	// LengthBE is the payload-length field read as a big-endian uint32.
	LengthBE uint32
	// LengthLE is the payload-length field read as a little-endian uint32.
	LengthLE uint32
	// LengthPlausible reports whether the length field, under the detected
	// order, is within MaxPayload.
	LengthPlausible bool
	// SwappedFields names the header fields whose little-endian and
	// big-endian readings differ. It is populated when the frame is detected
	// as little-endian, i.e. when the fields are actually byte-swapped
	// relative to the canonical wire order.
	SwappedFields []string
	// Reason is a one-line human explanation of the classification.
	Reason string
}

// AuditWireOrder fills an OrderAudit for b. It is total: short or empty input
// never panics and instead reports OrderUnknown with a reason.
func AuditWireOrder(b []byte) OrderAudit {
	a := OrderAudit{}
	if len(b) < HeaderSize {
		a.Reason = fmt.Sprintf("frame shorter than %d-byte header: %d bytes", HeaderSize, len(b))
		return a
	}

	magic := string(b[0:4])
	a.LengthBE = binary.BigEndian.Uint32(b[12:16])
	a.LengthLE = binary.LittleEndian.Uint32(b[12:16])
	a.MagicOK = magic == Magic
	a.Order = DetectWireOrder(b)

	switch a.Order {
	case OrderBigEndian:
		a.LengthPlausible = a.LengthBE <= MaxPayload
	case OrderLittleEndian:
		a.LengthPlausible = a.LengthLE <= MaxPayload
	}

	a.SwappedFields = swappedHeaderFields(b, a.Order)
	a.Reason = a.buildReason(magic)
	return a
}

// swappedHeaderFields lists the numeric header fields whose two byte-order
// readings differ. It is only meaningful for a frame detected as
// little-endian: for a big-endian frame the fields are already in wire order, so
// nothing is swapped and the list is empty.
func swappedHeaderFields(b []byte, order WireOrder) []string {
	if order != OrderLittleEndian {
		return nil
	}
	var out []string
	if binary.BigEndian.Uint32(b[8:12]) != binary.LittleEndian.Uint32(b[8:12]) {
		out = append(out, "sequence")
	}
	if binary.BigEndian.Uint32(b[12:16]) != binary.LittleEndian.Uint32(b[12:16]) {
		out = append(out, "payload_length")
	}
	if binary.BigEndian.Uint16(b[6:8]) != binary.LittleEndian.Uint16(b[6:8]) {
		out = append(out, "message_type")
	}
	return out
}

// buildReason explains how the frame was classified.
func (a OrderAudit) buildReason(magic string) string {
	switch a.Order {
	case OrderBigEndian:
		if a.MagicOK {
			return fmt.Sprintf("magic 'NCRS' intact; payload length %d is plausible in big-endian order", a.LengthBE)
		}
		return fmt.Sprintf("magic %q is reversed but payload length %d is plausible in big-endian order", magic, a.LengthBE)
	case OrderLittleEndian:
		if !a.MagicOK {
			return fmt.Sprintf("magic %q is the byte-reversed 'NCRS'; payload length %d is plausible in little-endian order", magic, a.LengthLE)
		}
		return fmt.Sprintf("magic 'NCRS' intact but length 0x%08x exceeds MaxPayload big-endian; little-endian read %d is plausible", a.LengthBE, a.LengthLE)
	default:
		return fmt.Sprintf("magic %q is neither 'NCRS' nor 'SRCN'; no length reading is plausible", magic)
	}
}

// String renders the audit as a single line suitable for a CLI or error
// message.
func (a OrderAudit) String() string {
	return fmt.Sprintf("wire order %s (magic ok=%v, length BE=%d LE=%d plausible=%v, swapped=[%s]): %s",
		a.Order, a.MagicOK, a.LengthBE, a.LengthLE, a.LengthPlausible, strings.Join(a.SwappedFields, ", "), a.Reason)
}

// Hint names the likely cause and fix for the detected order in one factual
// sentence. It is intended to be shown alongside a decode error.
func (o OrderAudit) Hint() string {
	switch o.Order {
	case OrderLittleEndian:
		return "peer serialised with host byte order; on s390x this passes by accident — route the field through encoding/binary.BigEndian"
	case OrderBigEndian:
		return "as expected: frame header is in big-endian wire order"
	default:
		return "no wire order detected; check the frame source and stream alignment before retrying"
	}
}
