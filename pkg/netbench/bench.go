package netbench

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ProbeResult holds network latency measurements.
type ProbeResult struct {
	Target       string
	TCPConnectMs float64
	TLSHandshakeMs float64
	HTTPStatus   int
	Success      bool
	Error        string
}

// ProbeEndpoint measures TCP and TLS handshake durations.
func ProbeEndpoint(urlStr string, tcpTarget string) ProbeResult {
	res := ProbeResult{Target: tcpTarget}

	// 1. TCP Connect
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", tcpTarget, 5*time.Second)
	if err != nil {
		res.Error = fmt.Sprintf("TCP dial failed: %v", err)
		return res
	}
	res.TCPConnectMs = float64(time.Since(t0).Microseconds()) / 1000.0
	conn.Close()

	// 2. HTTP/TLS GET
	client := http.Client{
		Timeout: 6 * time.Second,
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
