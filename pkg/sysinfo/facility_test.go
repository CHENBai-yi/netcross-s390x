package sysinfo

import (
	"encoding/json"
	"strings"
	"testing"
)

// realCPUInfoFixture is a byte-identical copy of /proc/cpuinfo from the
// LinuxONE (machine type 9175) guest this project is developed on. The
// `features` line uses a TAB before the colon and the `facilities` line lists
// the numeric STFLE bitmap; both details are load-bearing for the parser.
const realCPUInfoFixture = `vendor_id       : IBM/S390
# processors    : 2
bogomips per cpu: 28901.00
max thread id   : 0
features` + "\t" + `: esan3 zarch stfle msa ldisp eimm dfp edat etf3eh highgprs te vx vxd vxe gs vxe2 vxp sort dflt vxp2 nnpa sie 
facilities      : 0 1 2 3 4 6 7 8 9 12 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 30 31 32 33 34 35 36 37 38 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 57 58 59 60 61 69 70 71 72 73 74 75 76 77 80 81 82 84 85 86 87 129 130 131 133 134 135 146 147 148 150 151 152 155 156 165 168 170 192 193 194 196 197 198 199 200 201
cache0          : level=1 type=Data scope=Private size=128K line_size=256 associativity=8
cache1          : level=1 type=Instruction scope=Private size=128K line_size=256 associativity=8
cache2          : level=2 type=Unified scope=Private size=36864K line_size=256 associativity=18
cache3          : level=3 type=Unified scope=Shared size=368640K line_size=256 associativity=180
processor 0: version = FF,  identification = 09DFD8,  machine = 9175
processor 1: version = FF,  identification = 09DFD8,  machine = 9175

cpu number      : 0
physical id     : 0
core id         : 0
book id         : 0
drawer id       : 0
dedicated       : 0
address         : 0
siblings        : 1
cpu cores       : 1
version         : FF
identification  : 09DFD8
machine         : 9175
cpu MHz dynamic : 5508
cpu MHz static  : 5508

cpu number      : 1
physical id     : 1
core id         : 1
book id         : 1
drawer id       : 1
dedicated       : 0
address         : 1
siblings        : 1
cpu cores       : 1
version         : FF
identification  : 09DFD8
machine         : 9175
cpu MHz dynamic : 5508
cpu MHz static  : 5508
`

func TestFacilityWordPlacement(t *testing.T) {
	// Facility n lives in word n/64 at bit position n%64 counted from the
	// MSB. This is the STFLE bit-order test: a little-endian or LSB-first
	// implementation must fail.
	fs := NewFacilitySet([]int{0, 63, 64, 127, 128, 255})
	// Facility 128 sets only the MSB of word 2 and facility 255 sets only the
	// LSB of word 3; the two end words are not symmetric like words 0 and 1.
	want := []uint64{0x8000000000000001, 0x8000000000000001, 0x8000000000000000, 0x0000000000000001}
	got := fs.Words()
	if len(got) != len(want) {
		t.Fatalf("Words() returned %d words, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d = 0x%016x, want 0x%016x", i, got[i], want[i])
		}
	}
}

func TestFacilityOutOfRangeRejected(t *testing.T) {
	cases := [][]int{{256}, {300}, {-1}, {0, 256}, {-1, 63}, {255, 256}}
	for _, nums := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewFacilitySet(%v) did not reject out-of-range numbers", nums)
				}
			}()
			NewFacilitySet(nums)
		}()
	}
}

func TestFacilitySetEmpty(t *testing.T) {
	fs := NewFacilitySet(nil)
	if fs.Count() != 0 {
		t.Errorf("Count() = %d, want 0", fs.Count())
	}
	for _, n := range []int{0, 5, 42, 129, 255} {
		if fs.Has(n) {
			t.Errorf("Has(%d) = true on an empty set", n)
		}
	}
	if len(fs.Numbers()) != 0 {
		t.Errorf("Numbers() = %v, want empty", fs.Numbers())
	}
	if fs.Ranges() != "" {
		t.Errorf("Ranges() = %q, want empty", fs.Ranges())
	}
}

func TestFacilityPredicates(t *testing.T) {
	fs := NewFacilitySet([]int{5, 17, 18, 19, 20, 21, 42, 76, 129, 133, 134, 135, 148, 165})

	if !fs.HasVector() {
		t.Error("HasVector() false, want true (facility 129)")
	}
	if !fs.HasVectorPackedDecimal() {
		t.Error("HasVectorPackedDecimal() false, want true (facility 134)")
	}
	if !fs.HasVectorEnhancement1() {
		t.Error("HasVectorEnhancement1() false, want true (facility 135)")
	}
	if !fs.HasVectorEnhancement2() {
		t.Error("HasVectorEnhancement2() false, want true (facility 148)")
	}
	for level := 1; level <= 5; level++ {
		if !fs.HasMSA(level) {
			t.Errorf("HasMSA(%d) false, want true", level)
		}
	}
	if fs.HasMSA(0) || fs.HasMSA(6) {
		t.Error("HasMSA out of 1..5 returned true")
	}
	if !fs.HasDecimalFloatingPoint() {
		t.Error("HasDecimalFloatingPoint() false, want true (facility 42)")
	}
	if !fs.HasNNPA() {
		t.Error("HasNNPA() false, want true (facility 165)")
	}
	if !fs.HasSIE() {
		t.Error("HasSIE() false, want true (facility 5)")
	}
	if !fs.HasGuardedStorage() {
		t.Error("HasGuardedStorage() false, want true (facility 133)")
	}
	if !fs.HasMessageSecurityAssist() {
		t.Error("HasMessageSecurityAssist() false, want true (facility 76)")
	}

	empty := NewFacilitySet(nil)
	if empty.HasVector() || empty.HasVectorPackedDecimal() || empty.HasVectorEnhancement1() ||
		empty.HasVectorEnhancement2() || empty.HasDecimalFloatingPoint() || empty.HasNNPA() ||
		empty.HasSIE() || empty.HasGuardedStorage() || empty.HasMessageSecurityAssist() {
		t.Error("a predicate returned true on an empty facility set")
	}
	for level := 1; level <= 5; level++ {
		if empty.HasMSA(level) {
			t.Errorf("HasMSA(%d) true on an empty facility set", level)
		}
	}
}

func TestParseFeaturesRealLine(t *testing.T) {
	var value string
	for _, line := range strings.Split(realCPUInfoFixture, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "features") {
			value = strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:])
			break
		}
	}
	if value == "" {
		t.Fatal("fixture has no features line")
	}

	fs := ParseFeatures(value)
	if len(fs) != 22 {
		t.Errorf("ParseFeatures yielded %d features, want 22", len(fs))
	}
	for _, name := range []string{"zarch", "stfle", "vx", "vxe2", "nnpa", "dfp", "sie"} {
		if !fs.Has(name) {
			t.Errorf("Has(%q) false, want true", name)
		}
	}
	if fs.Has("nonexistent") {
		t.Error("Has(\"nonexistent\") true, want false")
	}
}

func TestFeatureSetNamesSorted(t *testing.T) {
	fs := ParseFeatures("vxe2 vx dfp zarch")
	names := fs.Names()
	want := []string{"dfp", "vx", "vxe2", "zarch"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestFacilitySummary(t *testing.T) {
	fs := NewFacilitySet([]int{5, 21, 42, 129, 134, 148, 165})
	s := fs.Summary()
	if s.Total != 7 {
		t.Errorf("Total = %d, want 7", s.Total)
	}
	if s.Words != 4 {
		t.Errorf("Words = %d, want 4", s.Words)
	}
	if !s.Vector || !s.VectorPackedDecimal || !s.VectorEnhancement2 || !s.NNPA ||
		!s.DFP || !s.SIE || !s.MSA5 {
		t.Errorf("summary flags not set: %+v", s)
	}
	if s.GuardedStorage {
		t.Error("GuardedStorage true, want false (facility 133 absent)")
	}
	if s.String() == "" {
		t.Error("String() returned an empty line")
	}
}

func TestFacilityMarshalJSON(t *testing.T) {
	fs := NewFacilitySet([]int{0, 1})
	data, err := json.Marshal(fs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["count"].(float64) != 2 {
		t.Errorf("count = %v, want 2", got["count"])
	}
	if got["ranges"] != "0-1" {
		t.Errorf("ranges = %v, want \"0-1\"", got["ranges"])
	}
}
