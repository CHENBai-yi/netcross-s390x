package netbench

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// ProbeResult holds individual network latency measurements.
type ProbeResult struct {
	Target         string  `json:"target"`
	TCPConnectMs   float64 `json:"tcp_connect_ms"`
	TLSHandshakeMs float64 `json:"tls_handshake_ms"`
	HTTPStatus     int     `json:"http_status"`
	Success        bool    `json:"success"`
	Error          string  `json:"error,omitempty"`
}

// SummaryReport holds aggregated benchmark metrics.
type SummaryReport struct {
	Timestamp      string        `json:"timestamp"`
	TotalProbes    int           `json:"total_probes"`
	Successful     int           `json:"successful"`
	Failed         int           `json:"failed"`
	AvgTCPConnect  float64       `json:"avg_tcp_connect_ms"`
	AvgTLSHandshake float64      `json:"avg_tls_handshake_ms"`
	Results        []ProbeResult `json:"results"`
}

// ProbeEndpoint measures TCP and TLS handshake durations for a single target.
func ProbeEndpoint(urlStr string, tcpTarget string, timeout time.Duration) ProbeResult {
	res := ProbeResult{Target: tcpTarget}

	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", tcpTarget, timeout)
	if err != nil {
		res.Error = fmt.Sprintf("TCP dial failed: %v", err)
		return res
	}
	res.TCPConnectMs = float64(time.Since(t0).Microseconds()) / 1000.0
	conn.Close()

	client := http.Client{
		Timeout: timeout + 2*time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		},
	}

	t1 := time.Now()
	resp, err := client.Get(urlStr)
	if err != nil {
		res.Error = fmt.Sprintf("HTTP get failed: %v", err)
		return res
	}
	defer resp.Body.Close()
	res.TLSHandshakeMs = float64(time.Since(t1).Microseconds()) / 1000.0
	res.HTTPStatus = resp.StatusCode
	res.Success = true

	return res
}

// RunBenchmark performs parallel latency probes against multiple endpoints.
func RunBenchmark(targets []struct{ URL, TCP string }, timeout time.Duration) SummaryReport {
	report := SummaryReport{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		TotalProbes: len(targets),
		Results:     make([]ProbeResult, len(targets)),
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	var totalTCP, totalTLS float64

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, url, tcp string) {
			defer wg.Done()
			r := ProbeEndpoint(url, tcp, timeout)
			mu.Lock()
			report.Results[idx] = r
			if r.Success {
				report.Successful++
				totalTCP += r.TCPConnectMs
				totalTLS += r.TLSHandshakeMs
			} else {
				report.Failed++
			}
			mu.Unlock()
		}(i, t.URL, t.TCP)
	}

	wg.Wait()

	if report.Successful > 0 {
		report.AvgTCPConnect = totalTCP / float64(report.Successful)
		report.AvgTLSHandshake = totalTLS / float64(report.Successful)
	}

	return report
}
