package netbench

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFamilyOf(t *testing.T) {
	cases := []struct {
		in   string
		want Family
	}{
		{"1.1.1.1:443", FamilyIPv4},
		{"1.1.1.1", FamilyIPv4},
		{"[2606:4700:4700::1111]:443", FamilyIPv6},
		{"2606:4700:4700::1111", FamilyIPv6},
		{"[::1]:80", FamilyIPv6},
		{"::1", FamilyIPv6},
		{"example.com:443", FamilyNone},
		{"localhost", FamilyNone},
		{"", FamilyNone},
	}
	for _, c := range cases {
		if got := FamilyOf(c.in); got != c.want {
			t.Errorf("FamilyOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsIPv6Literal(t *testing.T) {
	if !IsIPv6Literal("[::1]:9100") {
		t.Error("[::1]:9100 should be an IPv6 literal")
	}
	if IsIPv6Literal("1.1.1.1:443") {
		t.Error("1.1.1.1:443 is not IPv6")
	}
	if IsIPv6Literal("example.com") {
		t.Error("a hostname is not an IPv6 literal")
	}
}

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		in   string
		port int
		want string
	}{
		{"1.1.1.1:443", 80, "1.1.1.1:443"},
		{"1.1.1.1", 443, "1.1.1.1:443"},
		{"[2606:4700::1111]:443", 80, "[2606:4700::1111]:443"},
		{"2606:4700::1111", 443, "[2606:4700::1111]:443"},
		{"::1", 9100, "[::1]:9100"},
		{"[::1]", 9100, "[::1]:9100"},
		{"example.com", 443, "example.com:443"},
	}
	for _, c := range cases {
		if got := NormalizeHostPort(c.in, c.port); got != c.want {
			t.Errorf("NormalizeHostPort(%q, %d) = %q, want %q", c.in, c.port, got, c.want)
		}
	}
}

func TestNetworkFor(t *testing.T) {
	if NetworkFor(FamilyIPv4) != "tcp4" {
		t.Error("ipv4 should map to tcp4")
	}
	if NetworkFor(FamilyIPv6) != "tcp6" {
		t.Error("ipv6 should map to tcp6")
	}
	if NetworkFor(FamilyNone) != "tcp" {
		t.Error("unknown family should map to tcp")
	}
}

func TestProbeDualStackIPv4Only(t *testing.T) {
	// A v4 loopback listener: v6 will resolve (::1) but nothing listens on it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	res := ProbeDualStack(context.Background(), addr, 80, time.Second)
	if !res.HasIPv4 {
		t.Fatalf("expected IPv4 to resolve: %+v", res)
	}
	if res.IPv4RTT <= 0 {
		t.Fatalf("expected a successful v4 connect, got %+v", res)
	}
	if res.Preferred != FamilyIPv4 {
		t.Fatalf("preferred = %q, want ipv4", res.Preferred)
	}
}

func TestProbeDualStackIPv6Loopback(t *testing.T) {
	// Listen on ::1 explicitly. If the kernel has no IPv6 stack at all the
	// listener fails and the test is skipped rather than failing, since a
	// CI runner without IPv6 is a valid environment.
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback available: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Listener.Close()
	srv.Listener, err = net.Listen("tcp6", addr)
	if err != nil {
		t.Skipf("cannot rebind on %s: %v", addr, err)
	}
	srv.Start()
	defer srv.Close()

	res := ProbeDualStack(context.Background(), addr, 80, time.Second)
	if !res.HasIPv6 {
		t.Fatalf("expected IPv6 to resolve for %s: %+v", addr, res)
	}
	if res.IPv6RTT <= 0 {
		t.Fatalf("expected a successful v6 connect to %s, got %+v", addr, res)
	}
	if res.IPv6Addr == "" {
		t.Fatal("IPv6Addr not populated")
	}
	if !strings.HasPrefix(res.IPv6Addr, "[::1]:") {
		t.Fatalf("IPv6Addr = %q, want a bracketed loopback", res.IPv6Addr)
	}
}

func TestProbeDualStackUnresolvable(t *testing.T) {
	res := ProbeDualStack(context.Background(), "no-such-host.invalid:443", 443, 300*time.Millisecond)
	if res.Error == "" {
		t.Fatal("expected a resolve error")
	}
	if res.HasIPv4 || res.HasIPv6 {
		t.Fatalf("unresolvable host reported addresses: %+v", res)
	}
	if res.Preferred != FamilyNone {
		t.Fatalf("preferred = %q, want none", res.Preferred)
	}
}

func TestProbeDualStackClosedPort(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	res := ProbeDualStack(context.Background(), addr, 80, 300*time.Millisecond)
	if res.HasIPv4 != true {
		t.Fatalf("expected v4 to resolve: %+v", res)
	}
	if res.IPv4RTT != 0 {
		t.Fatalf("closed port reported an RTT: %+v", res)
	}
	if res.Error == "" {
		t.Fatal("expected a connect error")
	}
}

func TestRunConcurrentFamilyIPv4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := RunConcurrentFamily(context.Background(), srv.URL, Config{
		Requests: 4, Concurrency: 2, Timeout: 2 * time.Second,
	}, FamilyIPv4)
	if res.Succeeded != 4 {
		t.Fatalf("succeeded = %d, want 4 (errors: %v)", res.Succeeded, res.Errors)
	}
}

func TestRunConcurrentFamilyIPv6Unreachable(t *testing.T) {
	// A v4-only server addressed as tcp6 must fail rather than silently
	// falling back: that is the whole point of the -ipv6 flag.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	res := RunConcurrentFamily(context.Background(), srv.URL, Config{
		Requests: 2, Concurrency: 1, Timeout: 300 * time.Millisecond,
	}, FamilyIPv6)
	if res.Succeeded != 0 {
		t.Fatalf("tcp6 to a v4-only listener succeeded: %+v", res)
	}
	if res.Failed != 2 {
		t.Fatalf("failed = %d, want 2", res.Failed)
	}
}

func TestSupportsIPv6DoesNotPanic(t *testing.T) {
	// The answer depends on the host, so only assert that the call returns
	// and that a false result is consistent with a lack of global addresses.
	got := SupportsIPv6()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("InterfaceAddrs failed: %v", err)
	}
	hasGlobal := false
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.To4() == nil && !ipnet.IP.IsLoopback() && !ipnet.IP.IsLinkLocalUnicast() {
			hasGlobal = true
		}
	}
	if got != hasGlobal {
		t.Fatalf("SupportsIPv6() = %v, but a direct scan says %v", got, hasGlobal)
	}
	t.Logf("SupportsIPv6 = %v", got)
}
