package sysinfo

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// IPv6Interface is one row of /proc/net/if_inet6.
//
// The kernel file is the authoritative list of IPv6 addresses bound in the
// network namespace, including link-local ones that `net.InterfaceAddrs` will
// also report but that a naive "do I have IPv6?" check would misread. It is
// read directly rather than shelling out to `ip -6 addr` because the exporter
// runs unprivileged and must not depend on iproute2 being installed.
//
// Row format (no header):
//
//	<32 hex chars of address> <ifindex hex> <prefixlen hex> <scope hex> <flags hex> <ifname>
//
// Example:
//
//	fe8000000000000000a141fffeb2303a 03 40 20 80  enc1000
type IPv6Interface struct {
	Address   net.IP
	IfIndex   int
	PrefixLen int
	Scope     int
	Flags     int
	Name      string
}

// ScopeString renders the kernel scope value. 0x00 is global, 0x20 link-local,
// 0x10 host. Only the global case is usable for probing a remote target.
func (i IPv6Interface) ScopeString() string {
	switch i.Scope {
	case 0x00:
		return "global"
	case 0x10:
		return "host"
	case 0x20:
		return "link"
	case 0x40:
		return "site"
	case 0x80:
		return "compat"
	}
	return fmt.Sprintf("0x%02x", i.Scope)
}

// IsGlobal reports whether the address is a globally routable IPv6 address,
// i.e. not loopback and not link-local. This is the predicate that decides
// whether -ipv6 can work against a remote target.
func (i IPv6Interface) IsGlobal() bool {
	if i.Address == nil || i.Address.IsLoopback() || i.Address.IsLinkLocalUnicast() {
		return false
	}
	return i.Scope == 0x00
}

// DualStack summarizes the host's address-family capability.
type DualStack struct {
	// IPv6Enabled is true when at least one IPv6 address is bound, of any
	// scope. A host with only ::1 has the stack enabled but no v6 egress.
	IPv6Enabled bool `json:"ipv6_enabled"`
	// IPv6Global is true when at least one non-loopback, non-link-local
	// address is bound, i.e. the host can actually reach a remote v6 peer.
	IPv6Global bool `json:"ipv6_global"`
	// IPv4Enabled is true when at least one non-loopback IPv4 address is bound.
	IPv4Enabled bool `json:"ipv4_enabled"`
	// Preferred names the family that should be used for remote probes.
	Preferred string `json:"preferred"`
	// Interfaces is the parsed /proc/net/if_inet6 content.
	Interfaces []IPv6Interface `json:"interfaces,omitempty"`
	// IPv4Addrs lists the non-loopback IPv4 addresses.
	IPv4Addrs []string `json:"ipv4_addrs,omitempty"`
	// Error records a non-fatal failure to read the kernel tables.
	Error string `json:"error,omitempty"`
}

// DetectDualStack inspects the kernel tables to report address-family
// capability. It never returns an error: a container without /proc/net/if_inet6
// is a legitimate configuration and is reported as IPv6-disabled.
func DetectDualStack() DualStack {
	ds := DualStack{}

	if f, err := os.Open("/proc/net/if_inet6"); err == nil {
		ifaces, perr := ParseIfInet6(f)
		f.Close()
		if perr != nil {
			ds.Error = "parse /proc/net/if_inet6: " + perr.Error()
		}
		ds.Interfaces = ifaces
		ds.IPv6Enabled = len(ifaces) > 0
		for _, i := range ifaces {
			if i.IsGlobal() {
				ds.IPv6Global = true
				break
			}
		}
	} else {
		ds.Error = "open /proc/net/if_inet6: " + err.Error()
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			if v4 := ipnet.IP.To4(); v4 != nil {
				ds.IPv4Enabled = true
				ds.IPv4Addrs = append(ds.IPv4Addrs, v4.String())
			}
		}
	}

	switch {
	case ds.IPv6Global:
		ds.Preferred = "ipv6"
	case ds.IPv4Enabled:
		ds.Preferred = "ipv4"
	case ds.IPv6Enabled:
		ds.Preferred = "ipv6-loopback"
	default:
		ds.Preferred = "none"
	}
	return ds
}

// ParseIfInet6 parses the /proc/net/if_inet6 format. It is exported so tests
// can feed fixture rows, including the malformed ones the kernel never emits
// but a hostile or truncated file could.
func ParseIfInet6(r io.Reader) ([]IPv6Interface, error) {
	var out []IPv6Interface
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return out, fmt.Errorf("line %d: expected 6 fields, got %d", lineNo, len(fields))
		}
		if len(fields[0]) != 32 {
			return out, fmt.Errorf("line %d: address field is %d hex chars, want 32", lineNo, len(fields[0]))
		}
		raw, err := hex.DecodeString(fields[0])
		if err != nil {
			return out, fmt.Errorf("line %d: bad hex address: %w", lineNo, err)
		}
		ifindex, err := strconv.ParseInt(fields[1], 16, 32)
		if err != nil {
			return out, fmt.Errorf("line %d: bad ifindex %q: %w", lineNo, fields[1], err)
		}
		prefix, err := strconv.ParseInt(fields[2], 16, 32)
		if err != nil {
			return out, fmt.Errorf("line %d: bad prefix length %q: %w", lineNo, fields[2], err)
		}
		scope, err := strconv.ParseInt(fields[3], 16, 32)
		if err != nil {
			return out, fmt.Errorf("line %d: bad scope %q: %w", lineNo, fields[3], err)
		}
		flags, err := strconv.ParseInt(fields[4], 16, 32)
		if err != nil {
			return out, fmt.Errorf("line %d: bad flags %q: %w", lineNo, fields[4], err)
		}
		out = append(out, IPv6Interface{
			Address:   net.IP(raw),
			IfIndex:   int(ifindex),
			PrefixLen: int(prefix),
			Scope:     int(scope),
			Flags:     int(flags),
			Name:      fields[5],
		})
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// Summary renders a one-line dual-stack description for the CLI banner.
func (d DualStack) Summary() string {
	parts := make([]string, 0, 3)
	if d.IPv4Enabled {
		parts = append(parts, fmt.Sprintf("ipv4=%d addr", len(d.IPv4Addrs)))
	} else {
		parts = append(parts, "ipv4=none")
	}
	if d.IPv6Global {
		parts = append(parts, fmt.Sprintf("ipv6=global(%d iface)", len(d.Interfaces)))
	} else if d.IPv6Enabled {
		parts = append(parts, fmt.Sprintf("ipv6=local-only(%d iface)", len(d.Interfaces)))
	} else {
		parts = append(parts, "ipv6=disabled")
	}
	parts = append(parts, "preferred="+d.Preferred)
	return strings.Join(parts, " | ")
}
