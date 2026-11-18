package netbench

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Family identifies the address family a probe actually used.
type Family string

// Address families.
const (
	FamilyIPv4 Family = "ipv4"
	FamilyIPv6 Family = "ipv6"
	FamilyNone Family = "none"
)

// FamilyOf classifies a host or host:port string by address family without
// performing any I/O. A hostname that is not an IP literal is reported as
// FamilyNone because the answer depends on what DNS returns and on the
// happy-eyeballs ordering the resolver picks.
func FamilyOf(hostport string) Family {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return FamilyNone
	}
	if ip.To4() != nil {
		return FamilyIPv4
	}
	return FamilyIPv6
}

// IsIPv6Literal reports whether hostport contains an IPv6 address. Bracketed
// and bare literals are both accepted.
func IsIPv6Literal(hostport string) bool { return FamilyOf(hostport) == FamilyIPv6 }

// NormalizeHostPort renders a target in the bracketed form net.Dial expects
// for IPv6, leaving IPv4 and hostnames untouched. A bare IPv6 literal with no
// port gets the supplied default port; an already-bracketed value is passed
// through unchanged.
func NormalizeHostPort(hostport string, defaultPort int) string {
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		// Already host:port (v4, hostname, or [v6]:port).
		return hostport
	}
	host := strings.Trim(hostport, "[]")
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		// Bare IPv6 literal: it must be bracketed, and it needs a port.
		return net.JoinHostPort(host, strconv.Itoa(defaultPort))
	}
	if host != "" {
		return net.JoinHostPort(host, strconv.Itoa(defaultPort))
	}
	return hostport
}

// NetworkFor maps a Family onto the network string net.Dial accepts.
func NetworkFor(f Family) string {
	switch f {
	case FamilyIPv4:
		return "tcp4"
	case FamilyIPv6:
		return "tcp6"
	default:
		return "tcp"
	}
}

// DualStackResult reports whether a target is reachable over each family.
type DualStackResult struct {
	Target    string        `json:"target"`
	HasIPv4   bool          `json:"has_ipv4"`
	HasIPv6   bool          `json:"has_ipv6"`
	Preferred Family        `json:"preferred"`
	IPv4Addr  string        `json:"ipv4_addr,omitempty"`
	IPv6Addr  string        `json:"ipv6_addr,omitempty"`
	IPv4RTT   time.Duration `json:"ipv4_rtt_ns,omitempty"`
	IPv6RTT   time.Duration `json:"ipv6_rtt_ns,omitempty"`
	// HappyEyeballsMs is the measured penalty for trying IPv6 first when the
	// host has no usable IPv6 route: the resolver waits out the connect
	// timeout before falling back to IPv4. It is the number that justifies
	// setting -ipv6 only on hosts with a real v6 path.
	HappyEyeballsMs float64 `json:"happy_eyeballs_penalty_ms,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// ProbeDualStack resolves a target and times a TCP connect over each available
// family. A family that resolves but does not connect is reported with
// HasIPvX true and an error, which is the case that matters on a guest with a
// link-local IPv6 address and no global route: DNS says yes, the network says
// no, and a naive dual-stack client eats the timeout.
func ProbeDualStack(ctx context.Context, hostport string, defaultPort int, timeout time.Duration) DualStackResult {
	res := DualStackResult{Target: hostport, Preferred: FamilyNone}
	hostport = NormalizeHostPort(hostport, defaultPort)

	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		res.Error = "split host/port: " + err.Error()
		return res
	}

	// Resolve explicitly so the result can name the address that was used.
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		res.Error = "resolve: " + err.Error()
		return res
	}

	var v4, v6 []string
	for _, a := range addrs {
		if a.IP.To4() != nil {
			v4 = append(v4, a.IP.String())
		} else {
			v6 = append(v6, a.IP.String())
		}
	}
	res.HasIPv4 = len(v4) > 0
	res.HasIPv6 = len(v6) > 0

	if res.HasIPv4 {
		res.IPv4Addr = net.JoinHostPort(v4[0], port)
		res.IPv4RTT, res.Error = dialOnce(ctx, res.IPv4Addr, "tcp4", timeout)
		if res.IPv4RTT > 0 {
			res.Error = ""
		}
	}
	if res.HasIPv6 {
		res.IPv6Addr = net.JoinHostPort(v6[0], port)
		var v6err string
		res.IPv6RTT, v6err = dialOnce(ctx, res.IPv6Addr, "tcp6", timeout)
		if v6err != "" && res.Error == "" {
			res.Error = v6err
		}
	}

	switch {
	case res.HasIPv6 && res.IPv6RTT > 0 && (!res.HasIPv4 || res.IPv6RTT <= res.IPv4RTT):
		res.Preferred = FamilyIPv6
	case res.HasIPv4 && res.IPv4RTT > 0:
		res.Preferred = FamilyIPv4
	case res.HasIPv6:
		res.Preferred = FamilyIPv6
	default:
		res.Preferred = FamilyNone
	}

	// The penalty is how much slower the v6 path is than the v4 path when
	// both work; when only v4 works but v6 was advertised, it is the cost of
	// discovering that the hard way.
	if res.HasIPv6 && res.IPv6RTT == 0 && res.IPv4RTT > 0 {
		res.HappyEyeballsMs = float64(timeout.Microseconds()) / 1000.0
	} else if res.HasIPv6 && res.HasIPv4 && res.IPv6RTT > res.IPv4RTT {
		res.HappyEyeballsMs = float64((res.IPv6RTT - res.IPv4RTT).Microseconds()) / 1000.0
	}
	return res
}

func dialOnce(ctx context.Context, addr, network string, timeout time.Duration) (time.Duration, string) {
	d := net.Dialer{Timeout: timeout}
	t0 := time.Now()
	conn, err := d.DialContext(ctx, network, addr)
	elapsed := time.Since(t0)
	if err != nil {
		return 0, fmt.Sprintf("%s dial: %v", network, err)
	}
	conn.Close()
	return elapsed, ""
}

// RunConcurrentFamily is RunConcurrent with the address family pinned. It is
// the entry point the -ipv6 CLI flag uses.
func RunConcurrentFamily(ctx context.Context, url string, cfg Config, family Family) ConcurrentResult {
	cfg.Network = NetworkFor(family)
	res := RunConcurrent(ctx, url, cfg)
	res.Target = url
	return res
}

// SupportsIPv6 reports whether the kernel has at least one non-loopback IPv6
// address bound. It is a cheap check (no packets sent) used to fail fast with
// a clear message instead of letting every probe time out one by one.
func SupportsIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip.To4() != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return true
	}
	return false
}
