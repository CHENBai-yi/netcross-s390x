package sysinfo

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"
)

// maxSTFLEWords is the number of 64-bit doublewords the STFLE instruction can
// return on any currently shipped z/Architecture machine. STFLE reports the
// installed-facility bitmap in at most four doublewords (256 facility bits),
// so a facility number outside [0, 256) has nowhere to live and is a caller
// error rather than something to wrap around.
const maxSTFLEWords = 4

// FacilitySet is the installed-facility bitmap reported by the STFLE
// instruction, decoded from the numeric facility list in /proc/cpuinfo.
//
// Facility numbers follow the z/Architecture Principles of Operation, not any
// generic convention. A set bit means the facility is installed; an instruction
// that requires an absent facility traps with a specification exception when
// it is executed. The predicates below name the facilities that matter to
// NetCross's vector, decimal and virtualisation paths and document what traps
// without each one.
//
// Bit order is STFLE's: facility n lives in doubleword n/64 at bit position
// n%64 counted from the most-significant bit.
type FacilitySet struct {
	words []uint64
}

// NewFacilitySet builds a FacilitySet from a list of facility numbers.
//
// It panics on any number outside [0, 256): STFLE stores at most four
// doublewords, so an out-of-range number would have to be silently wrapped
// into word 0 or word 3, which would report a facility that is not installed.
func NewFacilitySet(nums []int) FacilitySet {
	fs := FacilitySet{words: make([]uint64, maxSTFLEWords)}
	for _, n := range nums {
		if n < 0 || n >= maxSTFLEWords*64 {
			panic(fmt.Sprintf("sysinfo: facility number %d out of range [0, %d)", n, maxSTFLEWords*64))
		}
		fs.words[n/64] |= 1 << (63 - uint(n%64))
	}
	return fs
}

// Has reports whether facility n is installed.
func (f FacilitySet) Has(n int) bool {
	if n < 0 || n >= len(f.words)*64 {
		return false
	}
	return f.words[n/64]&(1<<(63-uint(n%64))) != 0
}

// Count returns the number of installed facilities, i.e. the number of set
// bits in the bitmap.
func (f FacilitySet) Count() int {
	n := 0
	for _, w := range f.words {
		n += bits.OnesCount64(w)
	}
	return n
}

// Numbers returns the installed facility numbers in strictly ascending order.
func (f FacilitySet) Numbers() []int {
	var out []int
	for w, word := range f.words {
		for b := 0; b < 64; b++ {
			if word&(1<<(63-uint(b))) != 0 {
				out = append(out, w*64+b)
			}
		}
	}
	return out
}

// Ranges renders the installed facility numbers as a compact, comma-separated
// list of inclusive runs, e.g. "0-4,6-9,12".
func (f FacilitySet) Ranges() string {
	nums := f.Numbers()
	if len(nums) == 0 {
		return ""
	}
	parts := make([]string, 0, len(nums))
	start, prev := nums[0], nums[0]
	flush := func(s, e int) {
		if s == e {
			parts = append(parts, strconv.Itoa(s))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", s, e))
		}
	}
	for _, n := range nums[1:] {
		if n == prev+1 {
			prev = n
			continue
		}
		flush(start, prev)
		start, prev = n, n
	}
	flush(start, prev)
	return strings.Join(parts, ",")
}

// Words returns the raw STFLE bitmap doublewords, most-significant bit of
// word 0 first, for export.
func (f FacilitySet) Words() []uint64 {
	out := make([]uint64, len(f.words))
	copy(out, f.words)
	return out
}

// MarshalJSON renders the bitmap as a compact object so the field is useful in
// the JSON hardware profile instead of an empty struct.
func (f FacilitySet) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Count  int      `json:"count"`
		Ranges string   `json:"ranges"`
		Words  []uint64 `json:"words"`
	}{
		Count:  f.Count(),
		Ranges: f.Ranges(),
		Words:  f.Words(),
	})
}

// FeatureSet is the set of named CPU features reported on the `features` line
// of /proc/cpuinfo (zarch, stfle, vx, nnpa, ...).
type FeatureSet map[string]bool

// ParseFeatures splits a /proc/cpuinfo `features` value into a FeatureSet.
func ParseFeatures(s string) FeatureSet {
	fs := FeatureSet{}
	for _, tok := range strings.Fields(s) {
		fs[tok] = true
	}
	return fs
}

// Has reports whether the named feature is present.
func (f FeatureSet) Has(name string) bool { return f[name] }

// Names returns the feature names in sorted order.
func (f FeatureSet) Names() []string {
	names := make([]string, 0, len(f))
	for n := range f {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// HasVector reports whether facility 129 (Vector Facility) is installed.
// Without it every vector instruction (VL, VS, VA, VST, ...) traps with a
// specification exception.
func (f FacilitySet) HasVector() bool { return f.Has(129) }

// HasVectorPackedDecimal reports whether facility 134 (Vector Packed Decimal
// Facility) is installed. Without it the packed-decimal vector instructions
// (VDP, VSP, VMP, VTP, ...) trap even when facility 129 is present.
func (f FacilitySet) HasVectorPackedDecimal() bool { return f.Has(134) }

// HasVectorEnhancement1 reports whether facility 135 (Vector Enhancements
// Facility 1) is installed. Without it the VEF1 instructions (VN, VNN, VMSL,
// VFMAX, VFMIN, ...) trap.
func (f FacilitySet) HasVectorEnhancement1() bool { return f.Has(135) }

// HasVectorEnhancement2 reports whether facility 148 (Vector Enhancements
// Facility 2) is installed. Without it the VEF2 instructions (VLBR, VLER,
// VSLD, VSRD, ...) trap.
func (f FacilitySet) HasVectorEnhancement2() bool { return f.Has(148) }

// HasMSA reports whether Message Security Assist level `level` is installed.
// Levels 1..5 map to facilities 17..21. Without the matching facility the
// message-security instructions of that level (KMAC, KMF, KM, ...) trap.
func (f FacilitySet) HasMSA(level int) bool {
	switch level {
	case 1:
		return f.Has(17)
	case 2:
		return f.Has(18)
	case 3:
		return f.Has(19)
	case 4:
		return f.Has(20)
	case 5:
		return f.Has(21)
	default:
		return false
	}
}

// HasDecimalFloatingPoint reports whether facility 42 (Decimal Floating-Point
// Facility) is installed. Without it the decimal floating-point instructions
// (AD, SB, MV, ... and their DFP forms) trap.
func (f FacilitySet) HasDecimalFloatingPoint() bool { return f.Has(42) }

// HasNNPA reports whether facility 165 (Neural Network Processing Assist) is
// installed. Without it the NNPA vector instructions trap.
func (f FacilitySet) HasNNPA() bool { return f.Has(165) }

// HasSIE reports whether facility 5 (Supervisor Instruction Execution, SIE)
// is installed. Without it the SIE and SIE-assisted virtualisation
// instructions trap.
func (f FacilitySet) HasSIE() bool { return f.Has(5) }

// HasGuardedStorage reports whether facility 133 (Guarded Storage Facility)
// is installed. Without it the guarded-storage instructions (LGG, LGSC,
// LLGFSG, STGSC) trap, so pause-less garbage collection is unavailable.
func (f FacilitySet) HasGuardedStorage() bool { return f.Has(133) }

// HasMessageSecurityAssist reports whether facility 76 (Message Security
// Assist Extension 3) is installed. Without it the message-security-assist
// extension 3 instructions trap.
func (f FacilitySet) HasMessageSecurityAssist() bool { return f.Has(76) }

// FacilitySummary is a compact, human-readable roll-up of the facilities that
// matter to NetCross's vector, decimal and virtualisation paths.
type FacilitySummary struct {
	Total               int
	Words               int
	Vector              bool
	VectorPackedDecimal bool
	VectorEnhancement2  bool
	NNPA                bool
	DFP                 bool
	SIE                 bool
	GuardedStorage      bool
	MSA5                bool
}

// Summary rolls the facility bitmap up into a FacilitySummary.
func (f FacilitySet) Summary() FacilitySummary {
	return FacilitySummary{
		Total:               f.Count(),
		Words:               len(f.words),
		Vector:              f.HasVector(),
		VectorPackedDecimal: f.HasVectorPackedDecimal(),
		VectorEnhancement2:  f.HasVectorEnhancement2(),
		NNPA:                f.HasNNPA(),
		DFP:                 f.HasDecimalFloatingPoint(),
		SIE:                 f.HasSIE(),
		GuardedStorage:      f.HasGuardedStorage(),
		MSA5:                f.HasMSA(5),
	}
}

// String renders the summary as a single CLI line.
func (s FacilitySummary) String() string {
	return fmt.Sprintf("facilities=%d words=%d vector=%t vpd=%t ve2=%t nnpa=%t dfp=%t sie=%t gs=%t msa5=%t",
		s.Total, s.Words, s.Vector, s.VectorPackedDecimal, s.VectorEnhancement2,
		s.NNPA, s.DFP, s.SIE, s.GuardedStorage, s.MSA5)
}
