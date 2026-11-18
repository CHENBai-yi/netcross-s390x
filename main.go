package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/CHENBai-yi/netcross-s390x/pkg/endian"
	"github.com/CHENBai-yi/netcross-s390x/pkg/exporter"
	"github.com/CHENBai-yi/netcross-s390x/pkg/netbench"
	"github.com/CHENBai-yi/netcross-s390x/pkg/sysinfo"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "0.3.0"

// defaultTargets is the probe set used when -targets is not supplied. All
// three are anycast resolvers, so the measurement reflects the path from this
// host rather than one origin's capacity.
var defaultTargets = []exporter.Target{
	{URL: "https://1.1.1.1", TCP: "1.1.1.1:443", Name: "cloudflare-dns"},
	{URL: "https://8.8.8.8", TCP: "8.8.8.8:443", Name: "google-dns"},
	{URL: "https://9.9.9.9", TCP: "9.9.9.9:443", Name: "quad9-dns"},
}

type options struct {
	jsonOutput   bool
	timeoutSec   int
	listenAddr   string
	pollInterval int
	showVersion  bool

	concurrency int
	requests    int
	ipv6        bool
	format      string
	targets     string
	insecure    bool
}

func main() {
	var o options
	flag.BoolVar(&o.jsonOutput, "json", false, "Output results in JSON format (shorthand for -format json)")
	flag.IntVar(&o.timeoutSec, "timeout", 5, "Per-request timeout in seconds")
	flag.StringVar(&o.listenAddr, "listen", "", "Serve Prometheus metrics on address (e.g. :9100)")
	flag.IntVar(&o.pollInterval, "interval", 15, "Poll interval for metrics exporter in seconds")
	flag.BoolVar(&o.showVersion, "version", false, "Print version information and exit")
	flag.IntVar(&o.concurrency, "concurrency", 1, "Number of concurrent in-flight requests per target")
	flag.IntVar(&o.requests, "requests", 10, "Requests per target")
	flag.BoolVar(&o.ipv6, "ipv6", false, "Force IPv6 (tcp6) for all probes")
	flag.StringVar(&o.format, "format", "text", "Output format: text|json|prometheus")
	flag.StringVar(&o.targets, "targets", "", "Comma-separated host:port or URL targets (default: built-in resolver set)")
	flag.BoolVar(&o.insecure, "insecure", false, "Skip TLS certificate verification")
	flag.Parse()

	if o.showVersion {
		fmt.Printf("NetCross-s390x v%s (IBM LinuxONE & Multi-Arch Edition)\n", version)
		fmt.Printf("Runtime: %s\n", endian.Info())
		os.Exit(0)
	}

	if err := o.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "netcross: %v\n", err)
		os.Exit(2)
	}

	targets := defaultTargets
	if o.targets != "" {
		var err error
		targets, err = parseTargets(o.targets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "netcross: %v\n", err)
			os.Exit(2)
		}
	}

	timeout := time.Duration(o.timeoutSec) * time.Second
	profile := sysinfo.GetHardwareProfile()
	dual := sysinfo.DetectDualStack()

	// Mode 1: Prometheus exporter daemon.
	if o.listenAddr != "" {
		runDaemon(o, targets, dual)
		return
	}

	// Mode 2: one-shot prober.
	runProbe(o, targets, profile, dual, timeout)
}

func (o *options) validate() error {
	switch o.format {
	case "text", "json", "prometheus":
	default:
		return fmt.Errorf("invalid -format %q (want text, json or prometheus)", o.format)
	}
	if o.jsonOutput {
		o.format = "json"
	}
	if o.concurrency < 1 {
		return fmt.Errorf("-concurrency must be >= 1, got %d", o.concurrency)
	}
	if o.requests < 1 {
		return fmt.Errorf("-requests must be >= 1, got %d", o.requests)
	}
	if o.timeoutSec < 1 {
		return fmt.Errorf("-timeout must be >= 1 second, got %d", o.timeoutSec)
	}
	if o.pollInterval < 1 {
		return fmt.Errorf("-interval must be >= 1 second, got %d", o.pollInterval)
	}
	return nil
}

// parseTargets accepts "host:port" or "url" entries. A bare host:port gets an
// https:// URL and a name equal to the host:port; a full URL keeps its scheme
// and derives the TCP target from its authority.
func parseTargets(spec string) ([]exporter.Target, error) {
	var out []exporter.Target
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		t := exporter.Target{}
		if i := strings.Index(raw, "://"); i >= 0 {
			t.URL = raw
			rest := raw[i+3:]
			if j := strings.IndexAny(rest, "/?#"); j >= 0 {
				rest = rest[:j]
			}
			t.TCP = netbench.NormalizeHostPort(rest, 443)
			t.Name = rest
		} else {
			hostport := netbench.NormalizeHostPort(raw, 443)
			t.TCP = hostport
			t.URL = "https://" + hostport
			t.Name = raw
		}
		if t.TCP == "" {
			return nil, fmt.Errorf("cannot parse target %q", raw)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-targets contained no usable entries")
	}
	return out, nil
}

func runDaemon(o options, targets []exporter.Target, dual sysinfo.DualStack) {
	fmt.Println("==================================================")
	fmt.Println("  NetCross-s390x Prometheus Exporter Daemon Mode  ")
	fmt.Println("==================================================")
	fmt.Printf("Version      : v%s\n", version)
	fmt.Printf("Architecture : %s\n", endian.Info())
	fmt.Printf("Addressing   : %s\n", dual.Summary())
	fmt.Printf("Serving      : http://%s/metrics\n", o.listenAddr)
	fmt.Printf("Poll Interval: %d seconds\n", o.pollInterval)
	fmt.Printf("Concurrency  : %d\n", o.concurrency)
	fmt.Printf("Targets      : %d\n", len(targets))
	fmt.Println("--------------------------------------------------")

	family := netbench.Family("")
	if o.ipv6 {
		family = netbench.FamilyIPv6
		if !dual.IPv6Global {
			fmt.Println("[warn] -ipv6 requested but no global IPv6 address is bound;")
			fmt.Println("       probes will fail rather than fall back to IPv4.")
		}
	}

	exp := exporter.NewExporterWithOptions(exporter.Options{
		Targets:            targets,
		Interval:           time.Duration(o.pollInterval) * time.Second,
		Requests:           o.requests,
		Concurrency:        o.concurrency,
		Timeout:            time.Duration(o.timeoutSec) * time.Second,
		InsecureSkipVerify: o.insecure,
		Family:             family,
	})
	exp.Start()
	defer exp.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", exp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	log.Fatal(http.ListenAndServe(o.listenAddr, mux))
}

func runProbe(o options, targets []exporter.Target, profile sysinfo.HardwareProfile,
	dual sysinfo.DualStack, timeout time.Duration) {

	family := netbench.Family("")
	if o.ipv6 {
		family = netbench.FamilyIPv6
	}

	cfg := netbench.Config{
		Requests:           o.requests,
		Concurrency:        o.concurrency,
		Timeout:            timeout,
		InsecureSkipVerify: o.insecure,
	}
	// Bound the whole run: per-target budget times the number of targets, plus
	// slack for the sequential dual-stack probes.
	budget := timeout*time.Duration(o.requests+2)*time.Duration(len(targets)) + 10*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	results := make([]netbench.ConcurrentResult, 0, len(targets))
	dualResults := make([]netbench.DualStackResult, 0, len(targets))
	for _, t := range targets {
		var r netbench.ConcurrentResult
		if family != "" {
			r = netbench.RunConcurrentFamily(ctx, t.URL, cfg, family)
		} else {
			r = netbench.RunConcurrent(ctx, t.URL, cfg)
		}
		r.Target = t.Label()
		results = append(results, r)

		d := netbench.ProbeDualStack(ctx, t.TCP, 443, timeout)
		d.Target = t.Label()
		dualResults = append(dualResults, d)
	}
	total := netbench.AggregateTotal(results)

	switch o.format {
	case "json":
		emitJSON(profile, dual, results, dualResults, total)
	case "prometheus":
		emitPrometheus(results, dualResults)
	default:
		emitText(profile, dual, results, dualResults, total, o)
	}
}

func emitJSON(profile sysinfo.HardwareProfile, dual sysinfo.DualStack,
	results []netbench.ConcurrentResult, dualResults []netbench.DualStackResult,
	total netbench.ConcurrentResult) {

	// The v0.2 keys (hardware, endian, report) are kept at the top level so
	// existing consumers of `-json` keep parsing; the new detail is additive.
	out := map[string]interface{}{
		"version":      version,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"hardware":     profile,
		"endian":       endian.Info(),
		"dual_stack":   dual,
		"report":       total,
		"targets":      results,
		"dual_targets": dualResults,
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "netcross: marshal json: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// emitPrometheus reuses the exporter's renderer so the one-shot output is
// byte-identical to what the daemon serves for the same data. Without that the
// CLI and the daemon would drift and a test against one would say nothing
// about the other.
func emitPrometheus(results []netbench.ConcurrentResult, dualResults []netbench.DualStackResult) {
	exp := exporter.NewExporterWithOptions(exporter.Options{Interval: time.Hour})
	exp.WriteSnapshot(results, dualResults)
	fmt.Print(exp.Render())
}

func emitText(profile sysinfo.HardwareProfile, dual sysinfo.DualStack,
	results []netbench.ConcurrentResult, dualResults []netbench.DualStackResult,
	total netbench.ConcurrentResult, o options) {

	fmt.Println("======================================================================")
	fmt.Println("  NetCross-s390x: Multi-Arch Network Diagnostics")
	fmt.Println("======================================================================")
	fmt.Printf("Version           : v%s\n", version)
	fmt.Printf("Arch / Endianness : %s\n", endian.Info())
	fmt.Printf("CPU Topology      : %d Cores | Machine: %s\n", profile.NumCPU, profile.MachineType)
	fmt.Printf("Addressing        : %s\n", dual.Summary())
	fmt.Printf("Load              : %d requests/target | concurrency %d | timeout %ds\n",
		o.requests, o.concurrency, o.timeoutSec)
	if o.ipv6 {
		fmt.Println("Address family    : tcp6 (forced)")
	}
	fmt.Println("----------------------------------------------------------------------")

	fmt.Printf("%-18s %8s %8s %8s %8s %10s\n",
		"TARGET", "OK/REQ", "P50 ms", "P90 ms", "P99 ms", "req/s")
	for _, r := range results {
		fmt.Printf("%-18s %8s %8.2f %8.2f %8.2f %10.1f\n",
			truncate(r.Target, 18),
			fmt.Sprintf("%d/%d", r.Succeeded, r.Requests),
			r.Latency.P50, r.Latency.P90, r.Latency.P99, r.RPS)
	}

	fmt.Println("----------------------------------------------------------------------")
	fmt.Println("Latency distribution (end-to-end, milliseconds)")
	for _, r := range results {
		fmt.Printf("  %-16s %s\n", truncate(r.Target, 16), r.Latency)
	}

	// The phase breakdown is the point of the tool: on a mainframe guest the
	// number that moves is usually TTFB, not the handshake.
	fmt.Println("----------------------------------------------------------------------")
	fmt.Println("Phase breakdown (p50, milliseconds)")
	fmt.Printf("  %-16s %10s %10s %10s %10s %10s\n",
		"TARGET", "TCP", "TLS", "TTFB", "OTHER", "TOTAL")
	for _, r := range results {
		fmt.Printf("  %-16s %10.2f %10.2f %10.2f %10.2f %10.2f\n",
			truncate(r.Target, 16), r.TCP.P50, r.TLS.P50, r.TTFB.P50, r.Other.P50, r.Latency.P50)
	}
	fmt.Println("  (OTHER = total minus the four measured phases: request write,")
	fmt.Println("   HTTP/2 SETTINGS exchange, and scheduler time)")

	fmt.Println("----------------------------------------------------------------------")
	fmt.Println("Address family reachability")
	fmt.Printf("  %-16s %6s %6s %10s %10s\n", "TARGET", "IPv4", "IPv6", "v4 RTT", "v6 RTT")
	for _, d := range dualResults {
		fmt.Printf("  %-16s %6s %6s %10s %10s\n",
			truncate(d.Target, 16),
			yesNo(d.HasIPv4), yesNo(d.HasIPv6),
			msOrDash(d.IPv4RTT), msOrDash(d.IPv6RTT))
	}

	fmt.Println("----------------------------------------------------------------------")
	fmt.Printf("Summary: %d requests | %d ok | %d failed | %.1f%% success | %.1f req/s aggregate\n",
		total.Requests, total.Succeeded, total.Failed, total.SuccessRate, total.RPS)
	if len(total.Errors) > 0 {
		keys := make([]string, 0, len(total.Errors))
		for k := range total.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, total.Errors[k]))
		}
		fmt.Printf("Errors : %s\n", strings.Join(parts, " "))
	}
	fmt.Println("======================================================================")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}

func msOrDash(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f ms", float64(d.Microseconds())/1000.0)
}
