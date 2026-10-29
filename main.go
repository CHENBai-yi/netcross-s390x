package main

import (
	"fmt"
	"github.com/open-mainframe-mesh/netcross-s390x/pkg/endian"
	"github.com/open-mainframe-mesh/netcross-s390x/pkg/netbench"
)

func main() {
	fmt.Println("==================================================")
	fmt.Println("  NetCross-s390x: Multi-Arch Network Diagnostics  ")
	fmt.Println("==================================================")
	fmt.Println("System Environment:", endian.Info())
	fmt.Println("--------------------------------------------------")

	targets := []struct {
		url string
		tcp string
	}{
		{"https://1.1.1.1", "1.1.1.1:443"},
		{"https://8.8.8.8", "8.8.8.8:443"},
	}

	for _, t := range targets {
		fmt.Printf("Probing target: %s ...\n", t.tcp)
		r := netbench.ProbeEndpoint(t.url, t.tcp)
		if r.Success {
			fmt.Printf("  [OK] TCP Connect: %.2f ms | HTTP/TLS Total: %.2f ms | HTTP %d\n",
				r.TCPConnectMs, r.TLSHandshakeMs, r.HTTPStatus)
		} else {
			fmt.Printf("  [FAIL] Error: %s\n", r.Error)
		}
	}
	fmt.Println("==================================================")
}
