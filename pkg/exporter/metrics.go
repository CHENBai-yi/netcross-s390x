package exporter

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CHENBai-yi/netcross-s390x/pkg/endian"
	"github.com/CHENBai-yi/netcross-s390x/pkg/netbench"
	"github.com/CHENBai-yi/netcross-s390x/pkg/sysinfo"
)

// Target is one endpoint the exporter probes. It is a named struct rather than
// an anonymous one so callers outside this package can build a slice of them;
// an anonymous struct type in a function signature is not assignable from
// anywhere else.
type Target struct {
	// URL is the HTTP(S) endpoint used for the full phase timing.
	URL string `json:"url"`
	// TCP is the host:port used for the raw TCP connect timing.
	TCP string `json:"tcp"`
	// Name is the label value in the exported series. It defaults to TCP.
	Name string `json:"name,omitempty"`
}

// Label returns the label value for the target.
func (t Target) Label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.TCP
}

// Options configures an Exporter.
type Options struct {
	Targets []Target
	// Interval is the poll period.
	Interval time.Duration
	// Requests is how many requests each target receives per poll cycle.
	Requests int
	// Concurrency is the worker-pool size per target.
	Concurrency int
	// Timeout bounds a single request.
	Timeout time.Duration
	// InsecureSkipVerify disables TLS verification for the probe client.
	InsecureSkipVerify bool
	// Family pins the address family: netbench.FamilyIPv4/FamilyIPv6/"".
	Family netbench.Family
}

func (o Options) normalize() Options {
	if o.Interval <= 0 {
		o.Interval = 15 * time.Second
	}
	if o.Requests <= 0 {
		o.Requests = 5
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	return o
}

// Snapshot is one poll cycle's worth of results, kept under a mutex so the
// HTTP handler never races the collector.
type Snapshot struct {
	At        time.Time
	Results   []netbench.ConcurrentResult
	DualStack []netbench.DualStackResult
	Total     netbench.ConcurrentResult
	Polls     uint64
	Err       string
}

// Exporter manages Prometheus metrics collection and HTTP serving.
type Exporter struct {
	mu         sync.RWMutex
	snap       Snapshot
	opts       Options
	hardware   sysinfo.HardwareProfile
	dualStack  sysinfo.DualStack
	endianness string
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// NewExporter creates a metrics collector with default options.
func NewExporter(targets []Target, interval time.Duration) *Exporter {
	return NewExporterWithOptions(Options{Targets: targets, Interval: interval})
}

// NewExporterWithOptions creates a metrics collector with explicit options.
func NewExporterWithOptions(opts Options) *Exporter {
	opts = opts.normalize()
	return &Exporter{
		opts:       opts,
		hardware:   sysinfo.GetHardwareProfile(),
		dualStack:  sysinfo.DetectDualStack(),
		endianness: endian.Info(),
		stopCh:     make(chan struct{}),
	}
}

// Start begins periodic background probes. The first collection happens
// synchronously so a scrape immediately after startup has data.
func (e *Exporter) Start() {
	e.collect()
	go func() {
		ticker := time.NewTicker(e.opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				e.collect()
			case <-e.stopCh:
				return
			}
		}
	}()
}

// Stop terminates the periodic collection loop. It is safe to call twice.
func (e *Exporter) Stop() {
	e.stopOnce.Do(func() { close(e.stopCh) })
}

// Collect runs one poll cycle synchronously. Exported so tests and one-shot
// callers can populate the exporter without starting the goroutine.
func (e *Exporter) Collect() { e.collect() }

func (e *Exporter) collect() {
	ctx, cancel := context.WithTimeout(context.Background(),
		e.opts.Timeout*time.Duration(e.opts.Requests+2))
	defer cancel()

	cfg := netbench.Config{
		Requests:           e.opts.Requests,
		Concurrency:        e.opts.Concurrency,
		Timeout:            e.opts.Timeout,
		InsecureSkipVerify: e.opts.InsecureSkipVerify,
	}

	results := make([]netbench.ConcurrentResult, 0, len(e.opts.Targets))
	dual := make([]netbench.DualStackResult, 0, len(e.opts.Targets))
	for _, t := range e.opts.Targets {
		var r netbench.ConcurrentResult
		if e.opts.Family != "" && e.opts.Family != netbench.FamilyNone {
			r = netbench.RunConcurrentFamily(ctx, t.URL, cfg, e.opts.Family)
		} else {
			r = netbench.RunConcurrent(ctx, t.URL, cfg)
		}
		r.Target = t.Label()
		results = append(results, r)
		d := netbench.ProbeDualStack(ctx, t.TCP, 443, e.opts.Timeout)
		d.Target = t.Label()
		dual = append(dual, d)
	}

	e.mu.Lock()
	e.snap = Snapshot{
		At:        time.Now(),
		Results:   results,
		DualStack: dual,
		Total:     netbench.AggregateTotal(results),
		Polls:     e.snap.Polls + 1,
	}
	e.mu.Unlock()
}

// Snapshot returns a copy of the most recent poll for programmatic callers.
func (e *Exporter) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap
}

// WriteSnapshot replaces the stored snapshot with externally produced results.
// It exists so the one-shot CLI can render through exactly the same code path
// as the daemon: two renderers would drift, and a test against one would say
// nothing about the other.
func (e *Exporter) WriteSnapshot(results []netbench.ConcurrentResult,
	dual []netbench.DualStackResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.snap = Snapshot{
		At:        time.Now(),
		Results:   results,
		DualStack: dual,
		Total:     netbench.AggregateTotal(results),
		Polls:     e.snap.Polls + 1,
	}
}

// Render produces the Prometheus exposition text for the current snapshot.
func (e *Exporter) Render() string {
	e.mu.RLock()
	snap := e.snap
	e.mu.RUnlock()
	var b strings.Builder
	e.render(&b, snap)
	return b.String()
}

// Handler returns the HTTP handler serving Prometheus text exposition format.
func (e *Exporter) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(e.Render()))
	}
}

func (e *Exporter) render(b *strings.Builder, snap Snapshot) {
	// --- static hardware -------------------------------------------------
	writeHelp(b, "netcross_system_info", "Static hardware and architecture profile", "gauge")
	fmt.Fprintf(b, "netcross_system_info{arch=%q,machine_type=%q,num_cpu=%q,endianness=%q} 1\n\n",
		e.hardware.Arch, e.hardware.MachineType, strconv.Itoa(e.hardware.NumCPU), endiannessName())

	// --- address family capability ---------------------------------------
	writeHelp(b, "netcross_ipv6_available", "1 when a global-scope IPv6 address is bound and usable for remote probes", "gauge")
	fmt.Fprintf(b, "netcross_ipv6_available %d\n", boolToInt(e.dualStack.IPv6Global))

	writeHelp(b, "netcross_ipv6_enabled", "1 when any IPv6 address is bound, including link-local and loopback", "gauge")
	fmt.Fprintf(b, "netcross_ipv6_enabled %d\n", boolToInt(e.dualStack.IPv6Enabled))

	writeHelp(b, "netcross_ipv6_interfaces", "Number of IPv6 addresses bound in this network namespace", "gauge")
	fmt.Fprintf(b, "netcross_ipv6_interfaces %d\n\n", len(e.dualStack.Interfaces))

	writeHelp(b, "netcross_ipv4_available", "1 when a non-loopback IPv4 address is bound", "gauge")
	fmt.Fprintf(b, "netcross_ipv4_available %d\n\n", boolToInt(e.dualStack.IPv4Enabled))

	// --- counters (backwards compatible with v0.2) -----------------------
	writeHelp(b, "netcross_probes_total", "Total number of target probes executed in the last cycle", "counter")
	fmt.Fprintf(b, "netcross_probes_total %d\n\n", snap.Total.Requests)

	writeHelp(b, "netcross_probes_successful", "Total number of successful probes in the last cycle", "counter")
	fmt.Fprintf(b, "netcross_probes_successful %d\n\n", snap.Total.Succeeded)

	writeHelp(b, "netcross_probes_failed", "Total number of failed probes in the last cycle", "counter")
	fmt.Fprintf(b, "netcross_probes_failed %d\n\n", snap.Total.Failed)

	writeHelp(b, "netcross_poll_cycles_total", "Number of completed collection cycles since start", "counter")
	fmt.Fprintf(b, "netcross_poll_cycles_total %d\n\n", snap.Polls)

	// --- per-target series ------------------------------------------------
	writeHelp(b, "netcross_probe_success", "Target probe health status (1 = success, 0 = failed)", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_success{target=%q} %d\n", r.Target, boolToInt(r.Failed == 0 && r.Requests > 0))
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_success_rate", "Fraction of requests that completed with a 2xx-4xx status", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_success_rate{target=%q} %.4f\n", r.Target, r.SuccessRate/100)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_requests_per_second", "Sustained request throughput achieved during the cycle", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_requests_per_second{target=%q} %.2f\n", r.Target, r.RPS)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_concurrency", "Configured worker-pool size used for the target", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_concurrency{target=%q} %d\n", r.Target, r.Concurrency)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_requests", "Requests issued against the target in the last cycle", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_requests{target=%q} %d\n", r.Target, r.Requests)
	}
	b.WriteString("\n")

	// --- latency percentiles ---------------------------------------------
	writeHelp(b, "netcross_probe_latency_p50_seconds", "Median end-to-end request latency", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_latency_p50_seconds{target=%q} %.6f\n", r.Target, r.Latency.P50/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_latency_p90_seconds", "90th percentile end-to-end request latency", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_latency_p90_seconds{target=%q} %.6f\n", r.Target, r.Latency.P90/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_latency_p99_seconds", "99th percentile end-to-end request latency", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_latency_p99_seconds{target=%q} %.6f\n", r.Target, r.Latency.P99/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_latency_max_seconds", "Slowest observed request in the last cycle", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_latency_max_seconds{target=%q} %.6f\n", r.Target, r.Latency.Max/1000)
	}
	b.WriteString("\n")

	// --- per-phase timings ------------------------------------------------
	writeHelp(b, "netcross_tcp_connect_seconds", "TCP handshake duration (SYN to SYN-ACK)", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_tcp_connect_seconds{target=%q} %.6f\n", r.Target, r.TCP.P50/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_tls_handshake_seconds", "TLS handshake duration through Finished", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_tls_handshake_seconds{target=%q} %.6f\n", r.Target, r.TLS.P50/1000)
	}
	b.WriteString("\n")

	// v0.2 aliases. These were the series names before the phase timings were
	// generalised, and dashboards exist against them, so they are emitted in
	// parallel with the new names rather than renamed. They carry the same
	// value, so a query against either spelling returns the same number.
	writeHelp(b, "netcross_probe_tcp_connect_seconds", "Deprecated alias of netcross_tcp_connect_seconds", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_tcp_connect_seconds{target=%q} %.6f\n", r.Target, r.TCP.P50/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_tls_handshake_seconds", "Deprecated alias of netcross_tls_handshake_seconds", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_probe_tls_handshake_seconds{target=%q} %.6f\n", r.Target, r.TLS.P50/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_http_ttfb_seconds", "Time from completed handshake to first response byte", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_http_ttfb_seconds{target=%q} %.6f\n", r.Target, r.TTFB.P50/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_dns_resolve_seconds", "Name resolution duration (0 when the target is an IP literal)", "gauge")
	for _, r := range snap.Results {
		// DNS is not tracked as a percentile; it is either absent or a single
		// cached lookup, so the mean across successful requests is enough.
		fmt.Fprintf(b, "netcross_dns_resolve_seconds{target=%q} %.6f\n", r.Target, r.DNSMean/1000)
	}
	b.WriteString("\n")

	// --- per-phase tail percentiles -------------------------------------
	// P50 hides the failure mode this tool exists to expose: on a shared
	// LPAR a phase can look healthy at the median while its tail is
	// dominated by stolen vCPU cycles, so the P90/P99 tail of each phase is
	// emitted alongside the P50 above.
	//
	// A phase that was never measured is still emitted, at 0, rather than
	// skipped: in Prometheus an absent series reads as "no data for this
	// target", while a zero reads as "measured, and it took zero". The
	// companion netcross_phase_measured gauge is what lets a query tell the
	// two apart — a zero with measured=0 means "not measured", a zero with
	// measured=1 means "measured and genuinely zero".
	writeHelp(b, "netcross_phase_measured", "1 when the phase produced at least one non-zero measurement in the last cycle", "gauge")
	for _, r := range snap.Results {
		for _, ph := range []struct {
			name string
			p    netbench.Percentiles
		}{
			{"tcp", r.TCP},
			{"tls", r.TLS},
			{"ttfb", r.TTFB},
		} {
			fmt.Fprintf(b, "netcross_phase_measured{target=%q,phase=%q} %d\n",
				r.Target, ph.name, boolToInt(phaseMeasured(ph.p)))
		}
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_tcp_connect_p90_seconds", "90th percentile TCP handshake duration (SYN to SYN-ACK)", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_tcp_connect_p90_seconds{target=%q} %.6f\n", r.Target, r.TCP.P90/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_tcp_connect_p99_seconds", "99th percentile TCP handshake duration (SYN to SYN-ACK)", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_tcp_connect_p99_seconds{target=%q} %.6f\n", r.Target, r.TCP.P99/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_tls_handshake_p90_seconds", "90th percentile TLS handshake duration through Finished", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_tls_handshake_p90_seconds{target=%q} %.6f\n", r.Target, r.TLS.P90/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_tls_handshake_p99_seconds", "99th percentile TLS handshake duration through Finished", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_tls_handshake_p99_seconds{target=%q} %.6f\n", r.Target, r.TLS.P99/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_http_ttfb_p90_seconds", "90th percentile time from completed handshake to first response byte", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_http_ttfb_p90_seconds{target=%q} %.6f\n", r.Target, r.TTFB.P90/1000)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_http_ttfb_p99_seconds", "99th percentile time from completed handshake to first response byte", "gauge")
	for _, r := range snap.Results {
		fmt.Fprintf(b, "netcross_http_ttfb_p99_seconds{target=%q} %.6f\n", r.Target, r.TTFB.P99/1000)
	}
	b.WriteString("\n")

	// --- phase share of the round trip ----------------------------------
	// Each phase's P50 as a fraction of the end-to-end P50, so a query can
	// answer "which phase owns the round trip" directly instead of doing
	// arithmetic in Grafana. DNS is deliberately excluded: it is reported
	// as a mean, not a percentile, so it has no P50 to divide by. The ratio
	// is clamped to [0,1] because the per-phase P50s are not guaranteed to
	// sum to the end-to-end P50 — nearest-rank percentiles of different
	// distributions do not necessarily add, and a single phase can exceed
	// the total on its own.
	writeHelp(b, "netcross_phase_share_ratio", "Phase P50 as a fraction of end-to-end P50 latency, clamped to [0,1]", "gauge")
	for _, r := range snap.Results {
		total := r.Latency.P50
		for _, ph := range []struct {
			phase string
			p50   float64
		}{
			{"tcp", r.TCP.P50},
			{"tls", r.TLS.P50},
			{"ttfb", r.TTFB.P50},
			{"other", r.Other.P50},
		} {
			share := 0.0
			if total > 0 {
				share = ph.p50 / total
				if share > 1 {
					share = 1
				}
				if share < 0 {
					share = 0
				}
			}
			fmt.Fprintf(b, "netcross_phase_share_ratio{target=%q,phase=%q} %.6f\n",
				r.Target, ph.phase, share)
		}
	}
	b.WriteString("\n")

	// --- dual-stack per target --------------------------------------------
	writeHelp(b, "netcross_target_has_ipv4", "1 when the target resolves to an IPv4 address", "gauge")
	for _, d := range snap.DualStack {
		fmt.Fprintf(b, "netcross_target_has_ipv4{target=%q} %d\n", d.Target, boolToInt(d.HasIPv4))
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_target_has_ipv6", "1 when the target resolves to an IPv6 address", "gauge")
	for _, d := range snap.DualStack {
		fmt.Fprintf(b, "netcross_target_has_ipv6{target=%q} %d\n", d.Target, boolToInt(d.HasIPv6))
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_happy_eyeballs_penalty_seconds", "Extra latency paid when IPv6 is advertised but slower or unusable", "gauge")
	for _, d := range snap.DualStack {
		fmt.Fprintf(b, "netcross_happy_eyeballs_penalty_seconds{target=%q} %.6f\n",
			d.Target, d.HappyEyeballsMs/1000)
	}
	b.WriteString("\n")

	// --- error breakdown ---------------------------------------------------
	if len(snap.Total.Errors) > 0 {
		writeHelp(b, "netcross_probe_errors_total", "Failed probes by classified reason", "counter")
		keys := make([]string, 0, len(snap.Total.Errors))
		for k := range snap.Total.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable output ordering keeps diffs readable
		for _, k := range keys {
			fmt.Fprintf(b, "netcross_probe_errors_total{reason=%q} %d\n", k, snap.Total.Errors[k])
		}
		b.WriteString("\n")
	}

	// --- per-target error rate -------------------------------------------
	// Failed/Requests as a fraction, and each reason's share of the requests
	// issued, so a query can see both how often a target fails and why. Both
	// are emitted for every target even when the target had no failures, so
	// a zero error rate is a real zero rather than an absent series.
	writeHelp(b, "netcross_probe_error_rate", "Fraction of requests that failed, 0 when no requests were issued", "gauge")
	for _, r := range snap.Results {
		rate := 0.0
		if r.Requests > 0 {
			rate = float64(r.Failed) / float64(r.Requests)
		}
		fmt.Fprintf(b, "netcross_probe_error_rate{target=%q} %.6f\n", r.Target, rate)
	}
	b.WriteString("\n")

	writeHelp(b, "netcross_probe_error_ratio_by_reason", "Each classified error reason's share of the target's requests", "gauge")
	for _, r := range snap.Results {
		if len(r.Errors) == 0 {
			continue
		}
		keys := make([]string, 0, len(r.Errors))
		for k := range r.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable output ordering keeps diffs readable
		for _, k := range keys {
			ratio := 0.0
			if r.Requests > 0 {
				ratio = float64(r.Errors[k]) / float64(r.Requests)
			}
			fmt.Fprintf(b, "netcross_probe_error_ratio_by_reason{target=%q,reason=%q} %.6f\n",
				r.Target, k, ratio)
		}
	}
	b.WriteString("\n")

	// --- scrape metadata ---------------------------------------------------
	writeHelp(b, "netcross_last_collect_timestamp_seconds", "Unix time of the last completed collection cycle", "gauge")
	if snap.At.IsZero() {
		fmt.Fprintf(b, "netcross_last_collect_timestamp_seconds 0\n")
	} else {
		fmt.Fprintf(b, "netcross_last_collect_timestamp_seconds %d\n", snap.At.Unix())
	}
}

func writeHelp(b *strings.Builder, name, help, typ string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// phaseMeasured reports whether a phase produced at least one non-zero
// measurement in the last cycle. Count alone is not a sufficient signal: a
// plaintext target never fires the TLS handshake callbacks, so netbench still
// records one sample per successful request for the TLS phase — every one of
// them zero. Treating that as "measured" would report a plaintext target as
// having a measured zero-length TLS handshake, which is exactly the
// zero-vs-absent confusion netcross_phase_measured exists to prevent. A
// positive Max means at least one sample carried a real duration, i.e. the
// phase actually ran.
func phaseMeasured(p netbench.Percentiles) bool {
	return p.Max > 0
}

// endiannessName is a short tag for the system_info label, since the full
// endian.Info() string contains spaces and pipes that make a poor label.
func endiannessName() string {
	info := endian.Info()
	switch {
	case strings.Contains(info, "Big-Endian"):
		return "big"
	case strings.Contains(info, "Little-Endian"):
		return "little"
	}
	return "unknown"
}
