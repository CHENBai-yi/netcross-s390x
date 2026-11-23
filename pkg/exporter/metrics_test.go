package exporter

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CHENBai-yi/netcross-s390x/pkg/netbench"
)

// newLocalTarget starts a local HTTP server and returns a Target pointing at
// it, so the exporter tests never touch the public internet.
func newLocalTarget(t *testing.T, name string) (Target, *httptest.Server) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	return Target{URL: srv.URL, TCP: addr, Name: name}, srv
}

func scrape(t *testing.T, e *Exporter) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	e.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Fatalf("content type = %q, want Prometheus text 0.0.4", ct)
	}
	return rr.Body.String()
}

// metricValue pulls the value of an unlabelled metric line.
func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, name)), 64)
			if err != nil {
				t.Fatalf("%s: cannot parse value from %q: %v", name, line, err)
			}
			return v
		}
	}
	t.Fatalf("metric %s not found in scrape output", name)
	return 0
}

// labelledValue pulls the value of a metric line carrying a target label.
func labelledValue(t *testing.T, body, name, target string) float64 {
	t.Helper()
	needle := name + `{target="` + target + `"} `
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, needle) {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, needle)), 64)
			if err != nil {
				t.Fatalf("%s: cannot parse value from %q: %v", name, line, err)
			}
			return v
		}
	}
	t.Fatalf("metric %s{target=%q} not found", name, target)
	return 0
}

// metricWithLabels pulls the value of a metric line carrying an arbitrary
// label set, given the exact label string to match (for example
// `target="x",phase="tls"`). It is the general form of labelledValue for
// series whose labels go beyond a single target.
func metricWithLabels(t *testing.T, body, name, labels string) float64 {
	t.Helper()
	needle := name + "{" + labels + "} "
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, needle) {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, needle)), 64)
			if err != nil {
				t.Fatalf("%s: cannot parse value from %q: %v", name, line, err)
			}
			return v
		}
	}
	t.Fatalf("metric %s{%s} not found", name, labels)
	return 0
}

func newTestExporter(t *testing.T) (*Exporter, string) {
	t.Helper()
	tgt, _ := newLocalTarget(t, "local")
	e := NewExporterWithOptions(Options{
		Targets:     []Target{tgt},
		Interval:    time.Hour, // never fires during the test
		Requests:    4,
		Concurrency: 2,
		Timeout:     2 * time.Second,
	})
	e.Collect()
	return e, tgt.Name
}

// TestExporterBackwardsCompatibleMetrics guards the seven series that existed
// in v0.2. Removing or renaming any of them breaks existing dashboards, so
// this test fails loudly if the exporter's surface drifts.
func TestExporterBackwardsCompatibleMetrics(t *testing.T) {
	e, target := newTestExporter(t)
	body := scrape(t, e)

	legacy := []string{
		"netcross_system_info",
		"netcross_probes_total",
		"netcross_probes_successful",
		"netcross_probes_failed",
		"netcross_probe_tcp_connect_seconds",
		"netcross_probe_tls_handshake_seconds",
		"netcross_probe_success",
	}
	for _, name := range legacy {
		if !strings.Contains(body, name) {
			t.Errorf("legacy metric %s disappeared from the scrape", name)
		}
	}
	// The three per-target legacy series must still carry the target label.
	for _, name := range []string{
		"netcross_probe_tcp_connect_seconds",
		"netcross_probe_tls_handshake_seconds",
		"netcross_probe_success",
	} {
		_ = labelledValue(t, body, name, target)
	}
}

func TestExporterLatencyPercentiles(t *testing.T) {
	e, target := newTestExporter(t)
	body := scrape(t, e)

	p50 := labelledValue(t, body, "netcross_probe_latency_p50_seconds", target)
	p90 := labelledValue(t, body, "netcross_probe_latency_p90_seconds", target)
	p99 := labelledValue(t, body, "netcross_probe_latency_p99_seconds", target)

	if p50 <= 0 {
		t.Fatalf("p50 = %f, want > 0", p50)
	}
	if p90 < p50 {
		t.Errorf("p90 %f < p50 %f", p90, p50)
	}
	if p99 < p90 {
		t.Errorf("p99 %f < p90 %f", p99, p90)
	}
	// Seconds, not milliseconds: a localhost round trip is well under 1s.
	if p99 > 5 {
		t.Errorf("p99 = %f seconds, suspiciously large (units?)", p99)
	}
}

func TestExporterPhaseTimings(t *testing.T) {
	e, target := newTestExporter(t)
	body := scrape(t, e)

	tcp := labelledValue(t, body, "netcross_tcp_connect_seconds", target)
	if tcp <= 0 {
		t.Errorf("tcp connect = %f, want > 0", tcp)
	}
	// Plain HTTP: no TLS phase at all.
	if tls := labelledValue(t, body, "netcross_tls_handshake_seconds", target); tls != 0 {
		t.Errorf("tls handshake = %f for a plaintext target, want 0", tls)
	}
	ttfb := labelledValue(t, body, "netcross_http_ttfb_seconds", target)
	if ttfb < 0 {
		t.Errorf("ttfb = %f", ttfb)
	}
	if dns := labelledValue(t, body, "netcross_dns_resolve_seconds", target); dns < 0 {
		t.Errorf("dns = %f", dns)
	}
}

func TestExporterThroughputAndConcurrency(t *testing.T) {
	e, target := newTestExporter(t)
	body := scrape(t, e)

	if rps := labelledValue(t, body, "netcross_probe_requests_per_second", target); rps <= 0 {
		t.Errorf("rps = %f, want > 0", rps)
	}
	if c := labelledValue(t, body, "netcross_probe_concurrency", target); c != 2 {
		t.Errorf("concurrency = %f, want 2", c)
	}
	if r := labelledValue(t, body, "netcross_probe_requests", target); r != 4 {
		t.Errorf("requests = %f, want 4", r)
	}
	if sr := labelledValue(t, body, "netcross_probe_success_rate", target); sr != 1 {
		t.Errorf("success rate = %f, want 1", sr)
	}
	if s := labelledValue(t, body, "netcross_probe_success", target); s != 1 {
		t.Errorf("success = %f, want 1", s)
	}
}

func TestExporterIPv6Metrics(t *testing.T) {
	e, target := newTestExporter(t)
	body := scrape(t, e)

	// Host-level capability metrics must exist and be 0 or 1.
	for _, name := range []string{"netcross_ipv6_available", "netcross_ipv6_enabled", "netcross_ipv4_available"} {
		v := metricValue(t, body, name)
		if v != 0 && v != 1 {
			t.Errorf("%s = %f, want 0 or 1", name, v)
		}
	}
	if v := metricValue(t, body, "netcross_ipv6_interfaces"); v < 0 {
		t.Errorf("ipv6 interfaces = %f", v)
	}
	// Per-target family flags.
	if v := labelledValue(t, body, "netcross_target_has_ipv4", target); v != 1 {
		t.Errorf("target_has_ipv4 = %f, want 1 for 127.0.0.1", v)
	}
	_ = labelledValue(t, body, "netcross_target_has_ipv6", target)
	_ = labelledValue(t, body, "netcross_happy_eyeballs_penalty_seconds", target)
}

func TestExporterCountersMatchResults(t *testing.T) {
	tgt, _ := newLocalTarget(t, "local")
	// A second target that cannot connect, to exercise the failure path.
	dead := Target{URL: "http://127.0.0.1:1", TCP: "127.0.0.1:1", Name: "dead"}

	e := NewExporterWithOptions(Options{
		Targets: []Target{tgt, dead}, Interval: time.Hour,
		Requests: 3, Concurrency: 1, Timeout: 500 * time.Millisecond,
	})
	e.Collect()
	body := scrape(t, e)

	if total := metricValue(t, body, "netcross_probes_total"); total != 6 {
		t.Errorf("probes_total = %f, want 6", total)
	}
	succ := metricValue(t, body, "netcross_probes_successful")
	fail := metricValue(t, body, "netcross_probes_failed")
	if succ+fail != 6 {
		t.Errorf("successful(%f) + failed(%f) != 6", succ, fail)
	}
	if fail == 0 {
		t.Error("expected the dead target to contribute failures")
	}
	if v := labelledValue(t, body, "netcross_probe_success", "dead"); v != 0 {
		t.Errorf("dead target success = %f, want 0", v)
	}
	if !strings.Contains(body, "netcross_probe_errors_total") {
		t.Error("error histogram missing despite failures")
	}
}

func TestExporterHelpAndTypeLines(t *testing.T) {
	e, _ := newTestExporter(t)
	body := scrape(t, e)

	// Every exposed series must have a HELP and a TYPE line immediately
	// before its first sample, otherwise `promtool check metrics` rejects the
	// output.
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# HELP ") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				t.Errorf("malformed HELP line: %q", line)
				continue
			}
			seen["help:"+fields[2]] = true
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) != 4 {
				t.Errorf("malformed TYPE line: %q", line)
				continue
			}
			seen["type:"+fields[2]] = true
			switch fields[3] {
			case "gauge", "counter", "histogram", "summary", "untyped":
			default:
				t.Errorf("invalid metric type %q for %s", fields[3], fields[2])
			}
		}
	}
	for key := range seen {
		if strings.HasPrefix(key, "help:") {
			name := strings.TrimPrefix(key, "help:")
			if !seen["type:"+name] {
				t.Errorf("metric %s has HELP but no TYPE", name)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no metadata lines emitted")
	}
}

func TestExporterPollCycleCounter(t *testing.T) {
	e, _ := newTestExporter(t)
	if got := metricValue(t, scrape(t, e), "netcross_poll_cycles_total"); got != 1 {
		t.Fatalf("poll cycles after one Collect = %f, want 1", got)
	}
	e.Collect()
	if got := metricValue(t, scrape(t, e), "netcross_poll_cycles_total"); got != 2 {
		t.Fatalf("poll cycles after two Collects = %f, want 2", got)
	}
}

func TestExporterStartStop(t *testing.T) {
	tgt, _ := newLocalTarget(t, "local")
	e := NewExporterWithOptions(Options{
		Targets: []Target{tgt}, Interval: 50 * time.Millisecond,
		Requests: 1, Concurrency: 1, Timeout: time.Second,
	})
	e.Start()
	time.Sleep(180 * time.Millisecond)
	e.Stop()
	e.Stop() // must not panic on a double Stop

	snap := e.Snapshot()
	if snap.Polls < 2 {
		t.Fatalf("polls = %d after ~180ms at 50ms interval, want >= 2", snap.Polls)
	}
	if snap.At.IsZero() {
		t.Fatal("snapshot timestamp not set")
	}
}

func TestExporterStartPopulatesImmediately(t *testing.T) {
	tgt, _ := newLocalTarget(t, "local")
	e := NewExporterWithOptions(Options{
		Targets: []Target{tgt}, Interval: time.Hour,
		Requests: 1, Concurrency: 1, Timeout: time.Second,
	})
	e.Start()
	defer e.Stop()
	// Start collects synchronously before returning, so a scrape right away
	// must already have data rather than an empty report.
	if got := metricValue(t, scrape(t, e), "netcross_probes_total"); got != 1 {
		t.Fatalf("probes_total right after Start = %f, want 1", got)
	}
}

func TestTargetLabelDefaultsToTCP(t *testing.T) {
	if got := (Target{TCP: "1.1.1.1:443"}).Label(); got != "1.1.1.1:443" {
		t.Fatalf("default label = %q", got)
	}
	if got := (Target{TCP: "1.1.1.1:443", Name: "cloudflare"}).Label(); got != "cloudflare" {
		t.Fatalf("named label = %q", got)
	}
}

func TestEndiannessName(t *testing.T) {
	switch got := endiannessName(); got {
	case "big", "little", "unknown":
	default:
		t.Fatalf("endiannessName = %q", got)
	}
}

func TestNewExporterLegacyConstructor(t *testing.T) {
	// The v0.2 constructor signature must keep working for external callers.
	e := NewExporter([]Target{{URL: "http://127.0.0.1:1", TCP: "127.0.0.1:1"}}, time.Hour)
	if e.opts.Interval != time.Hour {
		t.Fatalf("interval = %v", e.opts.Interval)
	}
	if e.opts.Requests <= 0 || e.opts.Concurrency <= 0 {
		t.Fatalf("defaults not applied: %+v", e.opts)
	}
}

func TestSnapshotAggregate(t *testing.T) {
	tgt, _ := newLocalTarget(t, "local")
	e := NewExporterWithOptions(Options{
		Targets: []Target{tgt}, Interval: time.Hour,
		Requests: 2, Concurrency: 1, Timeout: time.Second,
	})
	e.Collect()
	snap := e.Snapshot()
	if len(snap.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(snap.Results))
	}
	if snap.Total.Requests != 2 {
		t.Fatalf("aggregate requests = %d, want 2", snap.Total.Requests)
	}
	if snap.Total.Target != "all" {
		t.Fatalf("aggregate target = %q", snap.Total.Target)
	}
}

func TestCollectWithCancelledContextDoesNotHang(t *testing.T) {
	// A very short timeout must produce a result, not a hang.
	e := NewExporterWithOptions(Options{
		Targets:  []Target{{URL: "http://10.255.255.1:81", TCP: "10.255.255.1:81", Name: "blackhole"}},
		Interval: time.Hour,
		Requests: 1, Concurrency: 1, Timeout: 100 * time.Millisecond,
	})
	done := make(chan struct{})
	go func() { e.Collect(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Collect did not return within 10s for an unreachable target")
	}
}

func TestExporterHandlerConcurrentScrapes(t *testing.T) {
	// The handler must be safe to call while a collection is in flight.
	e, _ := newTestExporter(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5; i++ {
			e.Collect()
		}
	}()
	for i := 0; i < 10; i++ {
		body := scrape(t, e)
		if !strings.Contains(body, "netcross_system_info") {
			t.Fatal("scrape lost its content under concurrency")
		}
	}
	<-done
}

func BenchmarkHandlerScrape(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	e := NewExporterWithOptions(Options{
		Targets:  []Target{{URL: srv.URL, TCP: strings.TrimPrefix(srv.URL, "http://"), Name: "bench"}},
		Interval: time.Hour, Requests: 1, Concurrency: 1, Timeout: time.Second,
	})
	e.Collect()
	req := httptest.NewRequest("GET", "/metrics", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		e.Handler().ServeHTTP(rr, req)
	}
}

// handResult builds a ConcurrentResult with fully known percentiles and error
// counts, so the renderer's arithmetic can be asserted exactly rather than
// against whatever the local loopback happened to measure. The phase P50s
// (5+6+8+1) exactly partition the end-to-end P50 (20), which the share-ratio
// test relies on.
func handResult(target string) netbench.ConcurrentResult {
	return netbench.ConcurrentResult{
		Target:      target,
		Concurrency: 2,
		Requests:    10,
		Succeeded:   9,
		Failed:      1,
		SuccessRate: 90,
		RPS:         5,
		Latency: netbench.Percentiles{
			Count: 9, Min: 10, P50: 20, P90: 40, P99: 60, Max: 80, Mean: 30,
		},
		TCP: netbench.Percentiles{
			Count: 9, Min: 1, P50: 5, P90: 9, P99: 15, Max: 20, Mean: 8,
		},
		TLS: netbench.Percentiles{
			Count: 9, Min: 2, P50: 6, P90: 12, P99: 20, Max: 30, Mean: 10,
		},
		TTFB: netbench.Percentiles{
			Count: 9, Min: 3, P50: 8, P90: 18, P99: 28, Max: 40, Mean: 14,
		},
		Other: netbench.Percentiles{
			Count: 9, Min: 1, P50: 1, P90: 1, P99: 1, Max: 1, Mean: 1,
		},
		Errors: map[string]int{"timeout": 1},
	}
}

func TestExporterPhaseTailPercentiles(t *testing.T) {
	e := NewExporterWithOptions(Options{Interval: time.Hour})
	e.WriteSnapshot([]netbench.ConcurrentResult{handResult("srv")}, nil)
	body := scrape(t, e)

	cases := []struct {
		metric string
		want   float64
	}{
		{"netcross_tcp_connect_p90_seconds", 0.009},
		{"netcross_tcp_connect_p99_seconds", 0.015},
		{"netcross_tls_handshake_p90_seconds", 0.012},
		{"netcross_tls_handshake_p99_seconds", 0.020},
		{"netcross_http_ttfb_p90_seconds", 0.018},
		{"netcross_http_ttfb_p99_seconds", 0.028},
	}
	for _, c := range cases {
		if got := labelledValue(t, body, c.metric, "srv"); got != c.want {
			t.Errorf("%s = %f, want %f", c.metric, got, c.want)
		}
	}

	// Tail ordering P99 >= P90 >= P50 for each phase, read back out of the
	// exposition text rather than recomputed from the input.
	for _, phase := range []struct {
		p50, p90, p99 string
	}{
		{"netcross_tcp_connect_seconds", "netcross_tcp_connect_p90_seconds", "netcross_tcp_connect_p99_seconds"},
		{"netcross_tls_handshake_seconds", "netcross_tls_handshake_p90_seconds", "netcross_tls_handshake_p99_seconds"},
		{"netcross_http_ttfb_seconds", "netcross_http_ttfb_p90_seconds", "netcross_http_ttfb_p99_seconds"},
	} {
		p50 := labelledValue(t, body, phase.p50, "srv")
		p90 := labelledValue(t, body, phase.p90, "srv")
		p99 := labelledValue(t, body, phase.p99, "srv")
		if p90 < p50 {
			t.Errorf("%s: p90 %f < p50 %f", phase.p90, p90, p50)
		}
		if p99 < p90 {
			t.Errorf("%s: p99 %f < p90 %f", phase.p99, p99, p90)
		}
	}
}

func TestExporterPhaseShareRatios(t *testing.T) {
	e := NewExporterWithOptions(Options{Interval: time.Hour})
	e.WriteSnapshot([]netbench.ConcurrentResult{handResult("srv")}, nil)
	body := scrape(t, e)

	want := map[string]float64{
		"tcp":   5.0 / 20.0,
		"tls":   6.0 / 20.0,
		"ttfb":  8.0 / 20.0,
		"other": 1.0 / 20.0,
	}
	sum := 0.0
	for phase, ratio := range want {
		got := metricWithLabels(t, body, "netcross_phase_share_ratio",
			`target="srv",phase="`+phase+`"`)
		if got-ratio > 1e-9 || ratio-got > 1e-9 {
			t.Errorf("share ratio phase=%s = %f, want %f", phase, got, ratio)
		}
		sum += got
	}
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("share ratios sum = %f, want 1.0 ± 0.001", sum)
	}
}

func TestExporterPhaseShareRatioClamping(t *testing.T) {
	// A phase whose P50 exceeds the end-to-end P50 must be clamped to 1,
	// not reported above 1.
	clamped := netbench.ConcurrentResult{
		Target:   "clamped",
		Requests: 10, Succeeded: 10, Failed: 0,
		Latency: netbench.Percentiles{Count: 10, P50: 10},
		TCP:     netbench.Percentiles{Count: 10, P50: 15}, // exceeds total
		TLS:     netbench.Percentiles{Count: 10, P50: 10},
		TTFB:    netbench.Percentiles{Count: 10, P50: 10},
		Other:   netbench.Percentiles{Count: 10, P50: 0},
	}
	e := NewExporterWithOptions(Options{Interval: time.Hour})
	e.WriteSnapshot([]netbench.ConcurrentResult{clamped}, nil)
	body := scrape(t, e)

	for _, phase := range []string{"tcp", "tls", "ttfb", "other"} {
		got := metricWithLabels(t, body, "netcross_phase_share_ratio",
			`target="clamped",phase="`+phase+`"`)
		if got > 1.0+1e-9 {
			t.Errorf("share ratio phase=%s = %f, want clamped to <= 1", phase, got)
		}
		if got < 0 {
			t.Errorf("share ratio phase=%s = %f, want >= 0", phase, got)
		}
	}
	// TCP's raw share is 15/10 = 1.5; it must render as exactly 1.
	if got := metricWithLabels(t, body, "netcross_phase_share_ratio",
		`target="clamped",phase="tcp"`); got != 1.0 {
		t.Errorf("tcp share = %f, want 1 (clamped from 1.5)", got)
	}
}

func TestExporterPhaseMeasuredZeroVsAbsent(t *testing.T) {
	// A plaintext target has no TLS phase: netbench records a zero TLS
	// duration for every request, so the TLS tail must still be present (at
	// 0) while phase_measured reports that it was never measured.
	plain := netbench.ConcurrentResult{
		Target:   "plain",
		Requests: 4, Succeeded: 4, Failed: 0,
		Latency: netbench.Percentiles{Count: 4, Min: 10, P50: 20, P90: 40, P99: 60, Max: 80, Mean: 30},
		TCP:     netbench.Percentiles{Count: 4, Min: 1, P50: 5, P90: 9, P99: 15, Max: 20, Mean: 8},
		TLS:     netbench.Percentiles{Count: 4, Min: 0, P50: 0, P90: 0, P99: 0, Max: 0, Mean: 0},
		TTFB:    netbench.Percentiles{Count: 4, Min: 3, P50: 8, P90: 18, P99: 28, Max: 40, Mean: 14},
		Other:   netbench.Percentiles{Count: 4, Min: 7, P50: 7, P90: 7, P99: 7, Max: 7, Mean: 7},
	}
	e := NewExporterWithOptions(Options{Interval: time.Hour})
	e.WriteSnapshot([]netbench.ConcurrentResult{plain}, nil)
	body := scrape(t, e)

	if got := metricWithLabels(t, body, "netcross_phase_measured",
		`target="plain",phase="tls"`); got != 0 {
		t.Errorf("phase_measured tls = %f, want 0 (plaintext target has no TLS phase)", got)
	}
	if got := metricWithLabels(t, body, "netcross_phase_measured",
		`target="plain",phase="tcp"`); got != 1 {
		t.Errorf("phase_measured tcp = %f, want 1", got)
	}
	// The TLS tail must still be present, at 0, not omitted.
	for _, m := range []string{
		"netcross_tls_handshake_p90_seconds",
		"netcross_tls_handshake_p99_seconds",
	} {
		if got := labelledValue(t, body, m, "plain"); got != 0 {
			t.Errorf("%s = %f, want 0 but still present", m, got)
		}
	}
}

func TestExporterProbeErrorRate(t *testing.T) {
	errTarget := netbench.ConcurrentResult{
		Target:   "err",
		Requests: 10, Succeeded: 7, Failed: 3,
		Errors: map[string]int{"timeout": 2, "eof": 1},
	}
	zeroTarget := netbench.ConcurrentResult{Target: "zero"} // 0 requests

	e := NewExporterWithOptions(Options{Interval: time.Hour})
	e.WriteSnapshot([]netbench.ConcurrentResult{errTarget, zeroTarget}, nil)
	body := scrape(t, e)

	if got := labelledValue(t, body, "netcross_probe_error_rate", "err"); got != 0.3 {
		t.Errorf("error rate = %f, want 0.3", got)
	}
	if got := labelledValue(t, body, "netcross_probe_error_rate", "zero"); got != 0 {
		t.Errorf("error rate for zero-request target = %f, want 0", got)
	}
	if got := metricWithLabels(t, body, "netcross_probe_error_ratio_by_reason",
		`target="err",reason="timeout"`); got != 0.2 {
		t.Errorf("error ratio by reason timeout = %f, want 0.2", got)
	}
	if got := metricWithLabels(t, body, "netcross_probe_error_ratio_by_reason",
		`target="err",reason="eof"`); got != 0.1 {
		t.Errorf("error ratio by reason eof = %f, want 0.1", got)
	}
	// No NaN/Inf from a divide by zero.
	for _, bad := range []string{"NaN", "+Inf", "-Inf"} {
		if strings.Contains(body, bad) {
			t.Errorf("scrape contains %q; error-rate math produced a non-finite value", bad)
		}
	}
}

func TestExporterPhaseMetricsRegression(t *testing.T) {
	// The pre-existing series must keep their exact values; dashboards and
	// saved queries depend on them.
	e := NewExporterWithOptions(Options{Interval: time.Hour})
	e.WriteSnapshot([]netbench.ConcurrentResult{handResult("srv")}, nil)
	body := scrape(t, e)

	if got := labelledValue(t, body, "netcross_probe_latency_p50_seconds", "srv"); got != 0.020 {
		t.Errorf("latency p50 = %f, want 0.020", got)
	}
	if got := labelledValue(t, body, "netcross_probe_success_rate", "srv"); got != 0.9 {
		t.Errorf("success rate = %f, want 0.9", got)
	}
	if got := labelledValue(t, body, "netcross_probe_tcp_connect_seconds", "srv"); got != 0.005 {
		t.Errorf("v0.2 tcp alias = %f, want 0.005", got)
	}
	if got := labelledValue(t, body, "netcross_probe_tls_handshake_seconds", "srv"); got != 0.006 {
		t.Errorf("v0.2 tls alias = %f, want 0.006", got)
	}
}

// TestExporterContextImport keeps the context import honest: the package must
// compile without it if Collect ever stops using a context, which would be a
// signal that timeouts are no longer bounded.
var _ = context.Background
var _ = netbench.FamilyIPv6
