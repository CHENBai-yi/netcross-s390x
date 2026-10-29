package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/CHENBai-yi/netcross-s390x/pkg/endian"
	"github.com/CHENBai-yi/netcross-s390x/pkg/exporter"
	"github.com/CHENBai-yi/netcross-s390x/pkg/netbench"
	"github.com/CHENBai-yi/netcross-s390x/pkg/sysinfo"
)

func main() {
	jsonOutput := flag.Bool("json", false, "Output results in JSON format")
	timeoutSec := flag.Int("timeout", 5, "Connection timeout in seconds")
	listenAddr := flag.String("listen", "", "Serve Prometheus metrics on address (e.g. :9100)")
	pollInterval := flag.Int("interval", 15, "Poll interval for metrics exporter in seconds")
	versionFlag := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println("NetCross-s390x v0.2.0 (IBM LinuxONE & Multi-Arch Edition)")
		fmt.Printf("Runtime: %s\n", endian.Info())
		os.Exit(0)
	}

	targets := []struct {
		URL string
		TCP string
	}{
		{"https://1.1.1.1", "1.1.1.1:443"},
		{"https://8.8.8.8", "8.8.8.8:443"},
		{"https://9.9.9.9", "9.9.9.9:443"},
	}

	// Mode 1: Prometheus Metrics HTTP Server
	if *listenAddr != "" {
		fmt.Println("==================================================")
		fmt.Println("  NetCross-s390x Prometheus Exporter Daemon Mode  ")
		fmt.Println("==================================================")
		fmt.Printf("Architecture : %s\n", endian.Info())
		fmt.Printf("Serving      : http://%s/metrics\n", *listenAddr)
		fmt.Printf("Poll Interval: %d seconds\n", *pollInterval)
		fmt.Println("--------------------------------------------------")

		exp := exporter.NewExporter(targets, time.Duration(*pollInterval)*time.Second)
		exp.Start()

		http.HandleFunc("/metrics", exp.Handler())
		http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok\n"))
		})

		log.Fatal(http.ListenAndServe(*listenAddr, nil))
		return
	}

	// Mode 2: One-shot CLI Prober
	profile := sysinfo.GetHardwareProfile()
	endianInfo := endian.Info()
	report := netbench.RunBenchmark(targets, time.Duration(*timeoutSec)*time.Second)

	if *jsonOutput {
		out := map[string]interface{}{
			"hardware": profile,
			"endian":   endianInfo,
			"report":   report,
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Println("==================================================")
	fmt.Println("  NetCross-s390x: Multi-Arch Network Diagnostics  ")
	fmt.Println("==================================================")
	fmt.Printf("Arch / Endianness : %s\n", endianInfo)
	fmt.Printf("CPU Topology      : %d Cores | Machine: %s\n", profile.NumCPU, profile.MachineType)
	fmt.Println("--------------------------------------------------")

	for _, r := range report.Results {
		if r.Success {
			fmt.Printf("  [OK] %-16s | TCP: %6.2f ms | TLS/HTTP: %6.2f ms | HTTP %d\n",
				r.Target, r.TCPConnectMs, r.TLSHandshakeMs, r.HTTPStatus)
		} else {
			fmt.Printf("  [FAIL] %-14s | Error: %s\n", r.Target, r.Error)
		}
	}

	fmt.Println("--------------------------------------------------")
	fmt.Printf("Summary: Total %d | Passed %d | Failed %d | Avg TCP: %.2f ms\n",
		report.TotalProbes, report.Successful, report.Failed, report.AvgTCPConnect)
	fmt.Println("==================================================")
}
