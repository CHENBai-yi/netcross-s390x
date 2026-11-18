package netbench

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests cover the original serial ProbeEndpoint / RunBenchmark path,
// concentrating on the error paths that the happy-path benchmark never
// exercises.

func TestProbeEndpointSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	res := ProbeEndpoint(srv.URL, addr, 3*time.Second)
	if !res.Success {
		t.Fatalf("probe failed: %s", res.Error)
	}
	if res.HTTPStatus != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", res.HTTPStatus, http.StatusNoContent)
	}
	if res.TCPConnectMs <= 0 {
		t.Fatalf("tcp connect = %f ms, want > 0", res.TCPConnectMs)
	}
	if res.Target != addr {
		t.Fatalf("target = %q, want %q", res.Target, addr)
	}
}

func TestProbeEndpointConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	res := ProbeEndpoint("http://"+addr, addr, 500*time.Millisecond)
	if res.Success {
		t.Fatal("probe of a closed port reported success")
	}
	if !strings.Contains(res.Error, "TCP dial failed") {
		t.Fatalf("error = %q, want a TCP dial failure", res.Error)
	}
	if res.HTTPStatus != 0 {
		t.Fatalf("status = %d, want 0 on dial failure", res.HTTPStatus)
	}
}

func TestProbeEndpointBadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	// TCP reachable but the URL scheme is unusable: the dial succeeds and the
	// HTTP stage is what fails.
	res := ProbeEndpoint("ht!tp://%%%", addr, time.Second)
	if res.Success {
		t.Fatal("malformed URL reported success")
	}
	if res.Error == "" {
		t.Fatal("expected a non-empty error")
	}
	if res.TCPConnectMs <= 0 {
		t.Fatalf("tcp connect should still have been measured, got %f", res.TCPConnectMs)
	}
}

func TestProbeEndpointTLSCertificateRejected(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")

	res := ProbeEndpoint(srv.URL, addr, 3*time.Second)
	if res.Success {
		t.Fatal("self-signed cert accepted; verification is off")
	}
	if !strings.Contains(strings.ToLower(res.Error), "certificate") &&
		!strings.Contains(strings.ToLower(res.Error), "x509") {
		t.Fatalf("error = %q, want a certificate complaint", res.Error)
	}
}

func TestRunBenchmarkMixedResults(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := l.Addr().String()
	l.Close()

	goodAddr := strings.TrimPrefix(good.URL, "http://")
	targets := []struct{ URL, TCP string }{
		{good.URL, goodAddr},
		{"http://" + dead, dead},
	}

	report := RunBenchmark(targets, 2*time.Second)
	if report.TotalProbes != 2 {
		t.Fatalf("total = %d, want 2", report.TotalProbes)
	}
	if report.Successful != 1 {
		t.Fatalf("successful = %d, want 1", report.Successful)
	}
	if report.Failed != 1 {
		t.Fatalf("failed = %d, want 1", report.Failed)
	}
	if report.AvgTCPConnect <= 0 {
		t.Fatalf("avg tcp = %f, want > 0", report.AvgTCPConnect)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(report.Results))
	}
	if report.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}
	// Result order must follow the input order despite the goroutines.
	if report.Results[0].Target != goodAddr {
		t.Fatalf("results are out of order: %+v", report.Results)
	}
}

func TestRunBenchmarkAllFailed(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := l.Addr().String()
	l.Close()

	report := RunBenchmark([]struct{ URL, TCP string }{{"http://" + dead, dead}}, time.Second)
	if report.Successful != 0 || report.Failed != 1 {
		t.Fatalf("report = %+v", report)
	}
	// Averages must stay at zero rather than dividing by zero successes.
	if report.AvgTCPConnect != 0 || report.AvgTLSHandshake != 0 {
		t.Fatalf("averages should be zero, got %+v", report)
	}
}

func TestRunBenchmarkEmptyTargets(t *testing.T) {
	report := RunBenchmark(nil, time.Second)
	if report.TotalProbes != 0 || len(report.Results) != 0 {
		t.Fatalf("empty run = %+v", report)
	}
}
