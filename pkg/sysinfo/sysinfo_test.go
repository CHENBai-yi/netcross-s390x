package sysinfo

import (
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

const ifInet6Fixture = `fe8000000000000000a141fffeb2303a 03 40 20 80  enc1000
00000000000000000000000000000001 01 80 10 80       lo
20010db8000000000000000000000001 03 40 00 00  enc1000
`

func TestParseIfInet6Fixture(t *testing.T) {
	ifaces, err := ParseIfInet6(strings.NewReader(ifInet6Fixture))
	if err != nil {
		t.Fatalf("ParseIfInet6: %v", err)
	}
	if len(ifaces) != 3 {
		t.Fatalf("parsed %d rows, want 3", len(ifaces))
	}

	link := ifaces[0]
	if !link.Address.Equal(net.ParseIP("fe80::a1:41ff:feb2:303a")) {
		t.Errorf("row 0 address = %s", link.Address)
	}
	if link.IfIndex != 3 {
		t.Errorf("row 0 ifindex = %d, want 3", link.IfIndex)
	}
	if link.PrefixLen != 64 {
		t.Errorf("row 0 prefix = %d, want 64", link.PrefixLen)
	}
	if link.Scope != 0x20 || link.ScopeString() != "link" {
		t.Errorf("row 0 scope = 0x%02x (%s), want link", link.Scope, link.ScopeString())
	}
	if link.Name != "enc1000" {
		t.Errorf("row 0 name = %q", link.Name)
	}
	if link.IsGlobal() {
		t.Error("a link-local address must not be reported as global")
	}

	loop := ifaces[1]
	if !loop.Address.Equal(net.ParseIP("::1")) {
		t.Errorf("row 1 address = %s", loop.Address)
	}
	if loop.ScopeString() != "host" {
		t.Errorf("row 1 scope = %s, want host", loop.ScopeString())
	}
	if loop.IsGlobal() {
		t.Error("::1 must not be reported as global")
	}

	glob := ifaces[2]
	if !glob.Address.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("row 2 address = %s", glob.Address)
	}
	if !glob.IsGlobal() {
		t.Error("2001:db8::1 with scope 0x00 must be reported as global")
	}
}

func TestParseIfInet6Empty(t *testing.T) {
	ifaces, err := ParseIfInet6(strings.NewReader("\n\n"))
	if err != nil {
		t.Fatalf("unexpected error on blank input: %v", err)
	}
	if len(ifaces) != 0 {
		t.Fatalf("got %d rows from blank input", len(ifaces))
	}
}

func TestParseIfInet6Malformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"too few fields", "fe8000000000000000a141fffeb2303a 03 40 20 80\n"},
		{"short address", "fe80 03 40 20 80 enc1000\n"},
		{"non-hex address", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz 03 40 20 80 enc1000\n"},
		{"bad ifindex", "fe8000000000000000a141fffeb2303a zz 40 20 80 enc1000\n"},
		{"bad prefix", "fe8000000000000000a141fffeb2303a 03 zz 20 80 enc1000\n"},
		{"bad scope", "fe8000000000000000a141fffeb2303a 03 40 zz 80 enc1000\n"},
		{"bad flags", "fe8000000000000000a141fffeb2303a 03 40 20 zz enc1000\n"},
	}
	for _, c := range cases {
		if _, err := ParseIfInet6(strings.NewReader(c.input)); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestParseIfInet6ReturnsPartialOnError(t *testing.T) {
	// The first row is valid, the second is not. The parser should hand back
	// what it managed to read so the caller can still report partial state.
	input := "fe8000000000000000a141fffeb2303a 03 40 20 80 enc1000\nbad row\n"
	ifaces, err := ParseIfInet6(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(ifaces) != 1 {
		t.Fatalf("partial parse returned %d rows, want 1", len(ifaces))
	}
}

func TestIPv6AddressIsParsedBigEndian(t *testing.T) {
	// /proc/net/if_inet6 stores the address as 32 hex chars in network order.
	// Assert the byte layout directly so a little-endian byte-reversal bug
	// cannot hide behind a round trip.
	raw, err := hex.DecodeString("20010db8000000000000000000000001")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01}
	for i := range want {
		if raw[i] != want[i] {
			t.Fatalf("byte %d = 0x%02x, want 0x%02x", i, raw[i], want[i])
		}
	}
	ip := net.IP(raw)
	if !ip.Equal(net.ParseIP("2001:db8::1")) {
		t.Fatalf("parsed address = %s", ip)
	}
	if ip.To4() != nil {
		t.Fatal("a v6 address must not report a v4 form")
	}
}

func TestScopeString(t *testing.T) {
	cases := map[int]string{
		0x00: "global",
		0x10: "host",
		0x20: "link",
		0x40: "site",
		0x80: "compat",
		0x07: "0x07",
	}
	for scope, want := range cases {
		got := IPv6Interface{Scope: scope}.ScopeString()
		if got != want {
			t.Errorf("scope 0x%02x = %q, want %q", scope, got, want)
		}
	}
}

func TestDetectDualStackOnThisHost(t *testing.T) {
	ds := DetectDualStack()
	t.Logf("dual stack: %s", ds.Summary())

	// Whatever the host has, the report must be internally consistent.
	if ds.IPv6Global && !ds.IPv6Enabled {
		t.Fatal("IPv6Global set without IPv6Enabled")
	}
	if ds.IPv6Enabled && len(ds.Interfaces) == 0 {
		t.Fatal("IPv6Enabled set with no interfaces parsed")
	}
	if ds.Preferred == "" {
		t.Fatal("Preferred is empty")
	}
	if ds.IPv6Global && ds.Preferred != "ipv6" {
		t.Fatalf("preferred = %q with a global v6 address", ds.Preferred)
	}
	if !ds.IPv6Global && ds.IPv4Enabled && ds.Preferred != "ipv4" {
		t.Fatalf("preferred = %q on an ipv4-only host", ds.Preferred)
	}
	if ds.Summary() == "" {
		t.Fatal("Summary is empty")
	}
}

func TestDualStackSummaryShapes(t *testing.T) {
	cases := []DualStack{
		{IPv4Enabled: true, IPv4Addrs: []string{"10.0.0.1"}, Preferred: "ipv4"},
		{IPv6Enabled: true, IPv6Global: true, IPv4Enabled: true, Preferred: "ipv6"},
		{IPv6Enabled: true, Preferred: "ipv6-loopback"},
		{Preferred: "none"},
	}
	for _, c := range cases {
		s := c.Summary()
		if s == "" {
			t.Fatalf("empty summary for %+v", c)
		}
		if !strings.Contains(s, "preferred="+c.Preferred) {
			t.Errorf("summary %q does not mention preferred=%s", s, c.Preferred)
		}
	}
}

// --- hardware profile ---------------------------------------------------

func TestGetHardwareProfile(t *testing.T) {
	p := GetHardwareProfile()
	if p.Arch == "" {
		t.Error("Arch is empty")
	}
	if p.NumCPU <= 0 {
		t.Errorf("NumCPU = %d", p.NumCPU)
	}
	if p.MachineType == "" {
		t.Error("MachineType is empty")
	}
	t.Logf("arch=%s cpus=%d machine=%s features=%d bytes",
		p.Arch, p.NumCPU, p.MachineType, len(p.Features))
}

func TestHardwareProfileOnS390x(t *testing.T) {
	p := GetHardwareProfile()
	if p.Arch != "s390x" {
		t.Skipf("not running on s390x (arch=%s)", p.Arch)
	}
	// On a real LinuxONE / z15 guest /proc/cpuinfo reports a numeric machine
	// type such as 9175, and the feature list includes the vector facility.
	if p.MachineType == "" {
		t.Fatal("s390x host reported no machine type")
	}
	if !strings.Contains(p.Features, "zarch") {
		t.Errorf("s390x feature list missing zarch: %q", p.Features)
	}
}

// --- STFLE facility parsing --------------------------------------------------

func TestParseCPUInfoFixture(t *testing.T) {
	p := ParseCPUInfo(strings.NewReader(realCPUInfoFixture))

	if p.MachineType != "9175" {
		t.Errorf("MachineType = %q, want 9175", p.MachineType)
	}
	// NumCPU is a runtime property; the parser must not derive it from the
	// "# processors" line.
	if p.NumCPU != 0 {
		t.Errorf("NumCPU = %d from ParseCPUInfo, want 0 (runtime-owned)", p.NumCPU)
	}

	if p.Facilities.Count() != 97 {
		t.Errorf("FacilitySet.Count() = %d, want 97", p.Facilities.Count())
	}
	if p.FacilityCount != 97 {
		t.Errorf("FacilityCount = %d, want 97", p.FacilityCount)
	}

	for _, n := range []int{129, 135, 165, 42, 134, 148, 133, 76} {
		if !p.Facilities.Has(n) {
			t.Errorf("Has(%d) = false, want true", n)
		}
	}
	// Facility 5 (SIE) and 10 are holes in this host's bitmap: the `features`
	// line names "sie", but bit 5 is not set in the STFLE list.
	if p.Facilities.Has(5) {
		t.Error("Has(5) = true, want false (5 is a hole in the facilities list)")
	}
	if p.Facilities.Has(10) {
		t.Error("Has(10) = true, want false (10 is a hole in the list, not shifted)")
	}

	nums := p.Facilities.Numbers()
	for i := 1; i < len(nums); i++ {
		if nums[i] <= nums[i-1] {
			t.Fatalf("Numbers() not strictly ascending at index %d: %d <= %d", i, nums[i], nums[i-1])
		}
	}

	want := "0-4,6-9,12,14-28,30-38,40-55,57-61,69-77,80-82,84-87,129-131,133-135,146-148,150-152,155-156,165,168,170,192-194,196-201"
	if got := p.Facilities.Ranges(); got != want {
		t.Errorf("Ranges() = %q, want %q", got, want)
	}
}

func TestParseCPUInfoIgnoresCacheAndProcessorLines(t *testing.T) {
	// cache0, processor 0, cpu number and cpu MHz dynamic all contain ':' and
	// one of them mentions "machine"; a naive prefix match misfires on them.
	// The parser must key on the exact top-level key only.
	input := `cache0          : level=1 type=Data scope=Private size=128K line_size=256 associativity=8
processor 0: version = FF,  identification = 09DFD8,  machine = 9175
cpu number      : 0
cpu MHz dynamic : 5508
machine         : 9175
`
	p := ParseCPUInfo(strings.NewReader(input))
	if p.MachineType != "9175" {
		t.Errorf("MachineType = %q, want 9175", p.MachineType)
	}
}

func TestParseCPUInfoEmpty(t *testing.T) {
	p := ParseCPUInfo(strings.NewReader(""))
	if p.MachineType != "generic-s390x" {
		t.Errorf("MachineType = %q, want generic-s390x", p.MachineType)
	}
	if p.Facilities.Count() != 0 {
		t.Errorf("Facilities.Count() = %d, want 0", p.Facilities.Count())
	}
}

func TestParseCPUInfoNoFacilities(t *testing.T) {
	// An amd64-style /proc/cpuinfo has no facilities line; the parser must
	// yield an empty FacilitySet rather than panic.
	input := `vendor_id       : GenuineIntel
model name      : Intel(R) Xeon(R) CPU
flags           : fpu vme de pse tsc msr pae
`
	p := ParseCPUInfo(strings.NewReader(input))
	if p.Facilities.Count() != 0 {
		t.Errorf("Facilities.Count() = %d, want 0", p.Facilities.Count())
	}
	if p.Facilities.Has(129) {
		t.Error("Has(129) = true on a host with no facilities line")
	}
	if p.FacilityCount != 0 {
		t.Errorf("FacilityCount = %d, want 0", p.FacilityCount)
	}
}
