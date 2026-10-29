package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// HardwareProfile summarizes mainframe hardware specifications.
type HardwareProfile struct {
	Arch         string `json:"arch"`
	NumCPU       int    `json:"num_cpu"`
	MachineType  string `json:"machine_type"`
	Features     string `json:"features"`
	Virtualization string `json:"virtualization"`
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

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "machine") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				p.MachineType = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "features") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				p.Features = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "type") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				p.Virtualization = strings.TrimSpace(parts[1])
			}
		}
	}

	if p.MachineType == "" {
		p.MachineType = fmt.Sprintf("IBM zSeries (%s)", runtime.GOARCH)
	}
	return p
}
