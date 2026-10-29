# NetCross-s390x 🚀

[![Multi-Arch Build & Test CI](https://github.com/CHENBai-yi/netcross-s390x/actions/workflows/ci.yml/badge.svg)](https://github.com/CHENBai-yi/netcross-s390x/actions/workflows/ci.yml)
[![Go Report Card](https://img.shields.io/badge/Go-1.22+-blue.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Architecture](https://img.shields.io/badge/Architecture-IBM%20s390x%20(Big--Endian)-purple.svg)]()
[![Prometheus Ready](https://img.shields.io/badge/Prometheus-Metrics%20Exporter-orange.svg)]()

**NetCross-s390x** is an open-source, production-grade network diagnostics engine, latency prober, and Prometheus metrics exporter tailored for **IBM Z and LinuxONE (`s390x`)** mainframes and heterogeneous hybrid-cloud environments.

---

## 🌟 Key Features

- **Native Big-Endian (`s390x`) Verification**: Dynamic runtime verification of memory alignment, pointer arithmetic, and IEEE 754 floating-point serialization on native IBM zSeries hardware.
- **Microsecond-Precision TCP/TLS Telemetry**: Measures sub-millisecond TCP handshake latency, TLS 1.3 negotiation speed, and HTTP status codes across distributed cloud regions.
- **Industrial Prometheus Exporter**: Embedded `/metrics` HTTP daemon exporting standards-compliant metrics for Grafana dashboards and Prometheus alerting.
- **Mainframe Hardware Discovery**: Deep inspection of `/proc/cpuinfo` parsing native IBM machine types (e.g. `z15 / LinuxONE III Model 9175`), cryptographic engines (`crypto/msa`), and on-chip neural processors (`nnpa`).
- **Continuous Integration for Linux on Z**: Built-in GitHub Actions matrix targeting `linux/s390x`, `linux/arm64`, and `linux/amd64`.

---

## 📋 Architecture & Data Flow

```
+------------------------------------------------------------------------+
|                         NetCross-s390x Engine                          |
+-----------------------------------+------------------------------------+
                                    |
            +-----------------------+-----------------------+
            |                                               |
   [Endianness & Hardware]                         [Active Prober Engine]
  - Big-Endian IEEE 754 check                     - Parallel TCP/TLS timing
  - Machine Type 9175 discovery                   - Multi-target jitter metrics
            |                                               |
            +-----------------------+-----------------------+
                                    |
                    +---------------+---------------+
                    |                               |
             [CLI / JSON Mode]             [Prometheus Daemon]
             One-shot diagnostics          Serves :9100/metrics
```

---

## 🛠️ Usage Guide

### 1. Run One-Shot Diagnostics (CLI)
```bash
./netcross
```

### 2. Export Structured JSON Report
```bash
./netcross -json
```

### 3. Launch Prometheus Exporter Daemon
```bash
./netcross -listen :9100 -interval 15
```

Fetch metrics anytime via:
```bash
curl http://localhost:9100/metrics
```

Example metrics output:
```text
# HELP netcross_system_info Static hardware and architecture profile
# TYPE netcross_system_info gauge
netcross_system_info{arch="s390x",machine_type="9175",num_cpu="2"} 1

# HELP netcross_probe_tcp_connect_seconds TCP handshake duration in seconds
# TYPE netcross_probe_tcp_connect_seconds gauge
netcross_probe_tcp_connect_seconds{target="1.1.1.1:443"} 0.008657
```

---

## 📄 License

Licensed under the [MIT License](./LICENSE). Contributions and PRs for mainframe Linux optimizations are warmly welcomed!
