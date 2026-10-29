# NetCross-s390x 🚀

[![Multi-Arch Build & Test CI](https://img.shields.io/badge/CI-Multi--Arch%20(s390x%20%2F%20amd64%20%2F%20arm64)-brightgreen.svg)]()
[![Go Report Card](https://img.shields.io/badge/Go-1.22+-blue.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Architecture](https://img.shields.io/badge/Architecture-IBM%20s390x%20(Big--Endian)-purple.svg)]()

**NetCross-s390x** is an open-source lightweight diagnostics and network latency benchmarking toolkit designed specifically for validating cross-platform networking, Big-Endian byte-order compatibility, and TLS handshake performance on **IBM Z and IBM LinuxONE (`s390x`)** mainframes and standard hybrid cloud environments.

---

## 🌟 Key Features

- **Native Big-Endian (`s390x`) Detection**: Dynamic runtime validation of memory alignment and byte ordering (distinguishing between Little-Endian and IBM LinuxONE native Big-Endian systems).
- **Sub-millisecond Precision Network Benchmarking**: Evaluates raw TCP connect latency, TLS 1.3 handshake negotiation time, and HTTP round-trip metrics.
- **Zero-Dependency Compilation**: Clean Go implementation with zero external heavy CGO dependencies, perfectly suited for containerized environments and CI/CD pipelines.
- **Continuous Integration for Linux on Z**: Built-in GitHub Actions matrix targeting `linux/s390x`, `linux/arm64`, and `linux/amd64`.

---

## 📋 Architecture & Design

```
+-------------------------------------------------------------+
|                     NetCross-s390x Engine                   |
+------------------------------+------------------------------+
                               |
            +------------------+------------------+
            |                                     |
   [Endianness Detector]                 [Latency Prober]
  - Validates pointer cast              - TCP Syn/Ack Timing
  - Distinguishes LE vs BE              - TLS 1.3 Handshake
  - Ensures s390x Memory Safety         - HTTP/2 Status Verification
```

---

## 🛠️ Quick Start

### 1. Build locally
```bash
go build -o netcross main.go
./netcross
```

### 2. Cross-compile for IBM LinuxONE (`s390x`)
```bash
GOOS=linux GOARCH=s390x go build -ldflags "-s -w" -o netcross-linux-s390x main.go
```

---

## 📄 License

This project is licensed under the [MIT License](./LICENSE). Contributions and pull requests for LinuxONE / s390x optimizations are welcome!
