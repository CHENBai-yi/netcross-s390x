package sysinfo

import (
	"bufio"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// HardwareProfile summarizes mainframe hardware specifications.
type HardwareProfile struct {
	Arch           string      `json:"arch"`
	NumCPU         int         `json:"num_cpu"`
	MachineType    string      `json:"machine_type"`
	Features       string      `json:"features"`
	FeatureSet     FeatureSet  `json:"feature_set,omitempty"`
	Facilities     FacilitySet `json:"facilities"`
	FacilityCount  int         `json:"facility_count,omitempty"`
	Virtualization string      `json:"virtualization"`
}

// ParseCPUInfo parses an s390x /proc/cpuinfo stream into a HardwareProfile.
//
// Only the top-level `machine`, `features`, `facilities` and `type` keys are
// recognised. The key is matched exactly after trimming, so lines whose values
// contain a colon (cache0, processor 0, cpu number, cpu MHz dynamic, ...) are
// ignored rather than misread by a naive prefix match.
//
// NumCPU is deliberately not derived from the file: it is a runtime property
// and is filled in by GetHardwareProfile.
func ParseCPUInfo(r io.Reader) HardwareProfile {
	p := HardwareProfile{}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch key {
		case "machine":
			p.MachineType = val
		case "features":
			p.Features = val
			p.FeatureSet = ParseFeatures(val)
		case "facilities":
			p.Facilities = parseFacilityLine(val)
		case "type":
			p.Virtualization = val
		}
	}

	if p.MachineType == "" {
		p.MachineType = "generic-s390x"
	}
	p.FacilityCount = p.Facilities.Count()
	return p
}

// parseFacilityLine decodes the numeric facility list on the `facilities`
// line. Non-numeric and out-of-range tokens are skipped, so a malformed or
// future /proc/cpuinfo cannot crash the process.
func parseFacilityLine(s string) FacilitySet {
	var nums []int
	for _, tok := range strings.Fields(s) {
		n, err := strconv.Atoi(tok)
		if err != nil {
			continue
		}
		if n >= 0 && n < maxSTFLEWords*64 {
			nums = append(nums, n)
		}
	}
	return NewFacilitySet(nums)
}

// GetHardwareProfile extracts s390x processor details from /proc/cpuinfo.
func GetHardwareProfile() HardwareProfile {
	p := HardwareProfile{
		Arch:   runtime.GOARCH,
		NumCPU: runtime.NumCPU(),
	}

	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		p.MachineType = "generic-s390x"
		return p
	}
	defer file.Close()

	p = ParseCPUInfo(file)
	p.Arch = runtime.GOARCH
	p.NumCPU = runtime.NumCPU()
	return p
}
