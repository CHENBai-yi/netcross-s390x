package netbench

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer starts a local HTTP server and returns it with its URL. Tests
// use a real listener rather than a mocked RoundTripper because the phase
// timings come from httptrace callbacks that only fire on real connections.
func newTestServer(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	// Silence the server-side TLS complaint that the certificate-rejection
	// test provokes on purpose; it is expected noise, not a test failure.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestRunConcurrentBasic(t *testing.T) {
	srv, hits := newTestServer(t)

	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests:    20,
		Concurrency: 4,
		Timeout:     5 * time.Second,
	})

	if res.Requests != 20 {
		t.Fatalf("requests = %d, want 20", res.Requests)
	}
	if res.Succeeded != 20 {
		t.Fatalf("succeeded = %d, want 20 (errors: %v)", res.Succeeded, res.Errors)
	}
	if res.Failed != 0 {
		t.Fatalf("failed = %d, want 0", res.Failed)
	}
	if got := atomic.LoadInt64(hits); got != 20 {
		t.Fatalf("server saw %d requests, want 20", got)
	}
	if res.Concurrency != 4 {
		t.Fatalf("concurrency = %d, want 4", res.Concurrency)
	}
	if res.RPS <= 0 {
		t.Fatalf("rps = %f, want > 0", res.RPS)
	}
	if res.SuccessRate != 100 {
		t.Fatalf("success rate = %f, want 100", res.SuccessRate)
	}
	if res.Latency.Count != 20 {
		t.Fatalf("latency sample count = %d, want 20", res.Latency.Count)
	}
	if res.Latency.P50 <= 0 {
		t.Fatalf("p50 = %f, want > 0", res.Latency.P50)
	}
}

func TestRunConcurrentPhaseTimingsArePopulated(t *testing.T) {
	srv, _ := newTestServer(t)
	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests: 5, Concurrency: 1, Timeout: 5 * time.Second,
	})
	if res.TCP.Count != 5 {
		t.Fatalf("tcp samples = %d, want 5", res.TCP.Count)
	}
	// Plain HTTP has no TLS phase, so it must be exactly zero rather than
	// being silently folded into TTFB.
	if res.TLS.P99 != 0 {
		t.Errorf("tls p99 = %f for a plaintext target, want 0", res.TLS.P99)
	}
	if res.TTFB.P50 <= 0 {
		t.Errorf("ttfb p50 = %f, want > 0", res.TTFB.P50)
	}
	// The total must be at least as large as any single phase.
	if res.Latency.P50 < res.TCP.P50 {
		t.Errorf("total p50 %f < tcp p50 %f", res.Latency.P50, res.TCP.P50)
	}
}

func TestRunConcurrentTLSPhases(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()

	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests:           5,
		Concurrency:        2,
		Timeout:            5 * time.Second,
		InsecureSkipVerify: true, // httptest uses a self-signed cert
	})
	if res.Succeeded != 5 {
		t.Fatalf("succeeded = %d, want 5 (errors: %v)", res.Succeeded, res.Errors)
	}
	if res.TLS.Count != 5 {
		t.Fatalf("tls samples = %d, want 5", res.TLS.Count)
	}
	if res.TLS.P50 <= 0 {
		t.Fatalf("tls handshake p50 = %f, want > 0", res.TLS.P50)
	}
}

func TestRunConcurrentTLSCertificateFailure(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()

	// Verification left on: the self-signed cert must be rejected and the
	// failure must be classified, not reported as a generic error.
	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests: 3, Concurrency: 1, Timeout: 5 * time.Second,
	})
	if res.Succeeded != 0 {
		t.Fatalf("succeeded = %d, want 0 with verification on", res.Succeeded)
	}
	if res.Failed != 3 {
		t.Fatalf("failed = %d, want 3", res.Failed)
	}
	if _, ok := res.Errors["tls_certificate"]; !ok {
		t.Fatalf("expected a tls_certificate bucket, got %v", res.Errors)
	}
}

func TestRunConcurrentConnectionRefused(t *testing.T) {
	// Grab a port, then close it so nothing is listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	res := RunConcurrent(context.Background(), "http://"+addr, Config{
		Requests: 3, Concurrency: 1, Timeout: 2 * time.Second,
	})
	if res.Succeeded != 0 {
		t.Fatalf("succeeded = %d, want 0", res.Succeeded)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected at least one error bucket")
	}
	if res.Latency.Count != 0 {
		t.Fatalf("failed requests must not contribute latency samples, got %d", res.Latency.Count)
	}
}

func TestRunConcurrentTimeout(t *testing.T) {
	// A handler that blocks past the client timeout.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(block)

	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests: 2, Concurrency: 2, Timeout: 150 * time.Millisecond,
	})
	if res.Succeeded != 0 {
		t.Fatalf("succeeded = %d, want 0", res.Succeeded)
	}
	if _, ok := res.Errors["timeout"]; !ok {
		t.Fatalf("expected a timeout bucket, got %v", res.Errors)
	}
}

func TestRunConcurrentDurationMode(t *testing.T) {
	srv, _ := newTestServer(t)
	res := RunConcurrent(context.Background(), srv.URL, Config{
		Duration:    200 * time.Millisecond,
		Concurrency: 2,
		Timeout:     2 * time.Second,
	})
	if res.Requests == 0 {
		t.Fatal("duration mode issued no requests")
	}
	// The wall clock must be close to the requested window; allow a wide
	// margin because a loaded CI runner can overshoot.
	if res.Duration < 150*time.Millisecond {
		t.Fatalf("duration = %v, want >= 150ms", res.Duration)
	}
	if res.Duration > 3*time.Second {
		t.Fatalf("duration = %v, wildly over the 200ms window", res.Duration)
	}
}

func TestRunConcurrentContextCancel(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the run starts

	res := RunConcurrent(ctx, srv.URL, Config{
		Requests: 1000, Concurrency: 4, Timeout: time.Second,
	})
	if res.Requests > 8 {
		t.Fatalf("cancelled run issued %d requests, want near zero", res.Requests)
	}
}

func TestRunConcurrentClampsConcurrency(t *testing.T) {
	srv, _ := newTestServer(t)
	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests: 2, Concurrency: 0, Timeout: time.Second,
	})
	if res.Concurrency != 1 {
		t.Fatalf("concurrency = %d, want clamped to 1", res.Concurrency)
	}
}

func TestRunConcurrentBadURL(t *testing.T) {
	res := RunConcurrent(context.Background(), "://not-a-url", Config{
		Requests: 2, Concurrency: 1, Timeout: time.Second,
	})
	if res.Succeeded != 0 {
		t.Fatalf("succeeded = %d, want 0 for a malformed URL", res.Succeeded)
	}
	if res.Failed != 2 {
		t.Fatalf("failed = %d, want 2", res.Failed)
	}
}

func TestRunConcurrentConcurrencyIsRespected(t *testing.T) {
	// Track the peak number of in-flight requests. With a pool of 4 the peak
	// must never exceed 4, and must actually reach it at least once,
	// otherwise the "concurrency" flag would be a no-op.
	var inFlight, peak int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
	}))
	defer srv.Close()

	res := RunConcurrent(context.Background(), srv.URL, Config{
		Requests: 12, Concurrency: 4, Timeout: 5 * time.Second,
	})
	if res.Requests != 12 {
		t.Fatalf("requests = %d, want 12", res.Requests)
	}
	if got := atomic.LoadInt64(&peak); got > 4 {
		t.Fatalf("peak in-flight = %d, exceeds the pool size of 4", got)
	}
	if got := atomic.LoadInt64(&peak); got < 2 {
		t.Fatalf("peak in-flight = %d, requests were effectively serial", got)
	}
}

// --- percentiles -------------------------------------------------------

func TestPercentileKnownDistribution(t *testing.T) {
	// 1..100: nearest-rank gives p50 = 50, p90 = 90, p99 = 99.
	samples := make([]float64, 100)
	for i := 0; i < 100; i++ {
		samples[i] = float64(i + 1)
	}
	p := Percentile(samples)
	if p.Count != 100 {
		t.Errorf("count = %d", p.Count)
	}
	if p.Min != 1 {
		t.Errorf("min = %v, want 1", p.Min)
	}
	if p.Max != 100 {
		t.Errorf("max = %v, want 100", p.Max)
	}
	if p.P50 != 50 {
		t.Errorf("p50 = %v, want 50", p.P50)
	}
	if p.P90 != 90 {
		t.Errorf("p90 = %v, want 90", p.P90)
	}
	if p.P99 != 99 {
		t.Errorf("p99 = %v, want 99", p.P99)
	}
	if p.Mean != 50.5 {
		t.Errorf("mean = %v, want 50.5", p.Mean)
	}
}

func TestPercentileSmallSamples(t *testing.T) {
	// With 1 sample every rank collapses onto it.
	p := Percentile([]float64{7})
	if p.P50 != 7 || p.P90 != 7 || p.P99 != 7 || p.Min != 7 || p.Max != 7 {
		t.Fatalf("single sample distribution = %+v", p)
	}
	// With 2 samples, p99 must not run off the end of the slice.
	p2 := Percentile([]float64{10, 20})
	if p2.P99 != 20 {
		t.Fatalf("p99 of 2 samples = %v, want 20", p2.P99)
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := Percentile(nil); got.Count != 0 || got.P50 != 0 {
		t.Fatalf("empty distribution = %+v", got)
	}
}

func TestPercentileUnsortedInput(t *testing.T) {
	p := Percentile([]float64{5, 1, 3, 2, 4})
	if p.Min != 1 || p.Max != 5 || p.P50 != 3 {
		t.Fatalf("unsorted distribution = %+v", p)
	}
}

func TestPercentileDoesNotMutateInput(t *testing.T) {
	in := []float64{3, 1, 2}
	_ = Percentile(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Fatalf("input was reordered: %v", in)
	}
}

func TestPercentileString(t *testing.T) {
	s := Percentile([]float64{1, 2, 3}).String()
	if !strings.Contains(s, "p50=") || !strings.Contains(s, "n=3") {
		t.Fatalf("unexpected rendering: %q", s)
	}
}

// --- phase timing helpers ---------------------------------------------

func TestPhaseTimingAddAndDiv(t *testing.T) {
	a := PhaseTiming{TCPConnect: 10 * time.Millisecond, TLSHandshake: 20 * time.Millisecond}
	b := PhaseTiming{TCPConnect: 30 * time.Millisecond, TLSHandshake: 40 * time.Millisecond}
	a.Add(b)
	if a.TCPConnect != 40*time.Millisecond || a.TLSHandshake != 60*time.Millisecond {
		t.Fatalf("Add = %+v", a)
	}
	avg := a.Div(2)
	if avg.TCPConnect != 20*time.Millisecond || avg.TLSHandshake != 30*time.Millisecond {
		t.Fatalf("Div(2) = %+v", avg)
	}
	if (PhaseTiming{}).Div(0) != (PhaseTiming{}) {
		t.Fatal("Div(0) should return the zero value")
	}
	if !strings.Contains(a.Millis(), "tcp=40.00ms") {
		t.Fatalf("Millis = %q", a.Millis())
	}
}

// --- multi-target / aggregate -----------------------------------------

func TestRunMultiTarget(t *testing.T) {
	srv1, _ := newTestServer(t)
	srv2, _ := newTestServer(t)
	res := RunMultiTarget(context.Background(), []string{srv1.URL, srv2.URL}, Config{
		Requests: 3, Concurrency: 2, Timeout: 2 * time.Second,
	})
	if len(res) != 2 {
		t.Fatalf("results = %d, want 2", len(res))
	}
	for i, r := range res {
		if r.Succeeded != 3 {
			t.Errorf("target %d succeeded = %d, want 3", i, r.Succeeded)
		}
	}
}

func TestAggregateTotal(t *testing.T) {
	in := []ConcurrentResult{
		{Requests: 10, Succeeded: 8, Failed: 2, RPS: 100, Duration: time.Second, Errors: map[string]int{"timeout": 2}},
		{Requests: 10, Succeeded: 10, RPS: 50, Duration: 2 * time.Second, Errors: map[string]int{}},
	}
	got := AggregateTotal(in)
	if got.Requests != 20 || got.Succeeded != 18 || got.Failed != 2 {
		t.Fatalf("aggregate = %+v", got)
	}
	if got.RPS != 150 {
		t.Fatalf("aggregate rps = %v, want 150", got.RPS)
	}
	if got.SuccessRate != 90 {
		t.Fatalf("aggregate success rate = %v, want 90", got.SuccessRate)
	}
	if got.Errors["timeout"] != 2 {
		t.Fatalf("aggregate errors = %v", got.Errors)
	}
	if got.Target != "all" {
		t.Fatalf("aggregate target = %q", got.Target)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{fmt.Errorf("dial tcp: %w", context.DeadlineExceeded), "timeout"},
		{fmt.Errorf("x509: certificate signed by unknown authority"), "tls_certificate"},
		{fmt.Errorf("remote error: tls: handshake failure"), "tls_handshake"},
		{fmt.Errorf("unexpected EOF"), "eof"},
		{fmt.Errorf("something else entirely"), "other"},
	}
	for _, c := range cases {
		if got := classifyError(c.err); got != c.want {
			t.Errorf("classifyError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func BenchmarkRunConcurrentLocal(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RunConcurrent(context.Background(), srv.URL, Config{
			Requests: 8, Concurrency: 4, Timeout: 2 * time.Second,
		})
	}
}
