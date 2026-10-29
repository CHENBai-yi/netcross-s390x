package exporter

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/CHENBai-yi/netcross-s390x/pkg/endian"
	"github.com/CHENBai-yi/netcross-s390x/pkg/netbench"
	"github.com/CHENBai-yi/netcross-s390x/pkg/sysinfo"
)

// Exporter manages Prometheus metrics collection and HTTP serving.
type Exporter struct {
	mu          sync.RWMutex
	lastReport  netbench.SummaryReport
	hardware    sysinfo.HardwareProfile
	endianness  string
	targets     []struct{ URL, TCP string }
	interval    time.Duration
	stopCh      chan struct{}
}

// NewExporter creates a new metrics collector instance.
func NewExporter(targets []struct{ URL, TCP string }, interval time.Duration) *Exporter {
	return &Exporter{
		targets:    targets,
		interval:   interval,
		hardware:   sysinfo.GetHardwareProfile(),
		endianness: endian.Info(),
		stopCh:     make(chan struct{}),
	}
}

// Start begins periodic background probes.
func (e *Exporter) Start() {
	e.collect()
	ticker := time.NewTicker(e.interval)
	go func() {
		for {
			select {
			case <-ticker.C:
				e.collect()
			case <-e.stopCh:
				ticker.Stop()
				return
			}
		}
	}()
}

// Stop terminates the periodic collection loop.
func (e *Exporter) Stop() {
	close(e.stopCh)
}

func (e *Exporter) collect() {
	report := netbench.RunBenchmark(e.targets, 5*time.Second)
	e.mu.Lock()
	e.lastReport = report
	e.mu.Unlock()
}

// Handler returns the HTTP handler serving standard Prometheus metrics.
func (e *Exporter) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e.mu.RLock()
		report := e.lastReport
		e.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		// 1. System Info Metric
		fmt.Fprintf(w, "# HELP netcross_system_info Static hardware and architecture profile\n")
		fmt.Fprintf(w, "# TYPE netcross_system_info gauge\n")
		fmt.Fprintf(w, "netcross_system_info{arch=\"%s\",machine_type=\"%s\",num_cpu=\"%d\"} 1\n\n",
			e.hardware.Arch, e.hardware.MachineType, e.hardware.NumCPU)

		// 2. Metrics summary
		fmt.Fprintf(w, "# HELP netcross_probes_total Total number of target probes executed\n")
		fmt.Fprintf(w, "# TYPE netcross_probes_total counter\n")
		fmt.Fprintf(w, "netcross_probes_total %d\n\n", report.TotalProbes)

		fmt.Fprintf(w, "# HELP netcross_probes_successful Total number of successful probes\n")
		fmt.Fprintf(w, "# TYPE netcross_probes_successful counter\n")
		fmt.Fprintf(w, "netcross_probes_successful %d\n\n", report.Successful)

		fmt.Fprintf(w, "# HELP netcross_probes_failed Total number of failed probes\n")
		fmt.Fprintf(w, "# TYPE netcross_probes_failed counter\n")
		fmt.Fprintf(w, "netcross_probes_failed %d\n\n", report.Failed)

		// 3. Per-target metrics
		fmt.Fprintf(w, "# HELP netcross_probe_tcp_connect_seconds TCP handshake duration in seconds\n")
		fmt.Fprintf(w, "# TYPE netcross_probe_tcp_connect_seconds gauge\n")
		for _, res := range report.Results {
			val := res.TCPConnectMs / 1000.0
			fmt.Fprintf(w, "netcross_probe_tcp_connect_seconds{target=\"%s\"} %.6f\n", res.Target, val)
		}
		fmt.Fprintf(w, "\n")

		fmt.Fprintf(w, "# HELP netcross_probe_tls_handshake_seconds Total HTTP/TLS round-trip duration in seconds\n")
		fmt.Fprintf(w, "# TYPE netcross_probe_tls_handshake_seconds gauge\n")
		for _, res := range report.Results {
			val := res.TLSHandshakeMs / 1000.0
			fmt.Fprintf(w, "netcross_probe_tls_handshake_seconds{target=\"%s\"} %.6f\n", res.Target, val)
		}
		fmt.Fprintf(w, "\n")

		fmt.Fprintf(w, "# HELP netcross_probe_success Target probe health status (1 = success, 0 = failed)\n")
		fmt.Fprintf(w, "# TYPE netcross_probe_success gauge\n")
		for _, res := range report.Results {
			status := 0
			if res.Success {
				status = 1
			}
			fmt.Fprintf(w, "netcross_probe_success{target=\"%s\"} %d\n", res.Target, status)
		}
	}
}
