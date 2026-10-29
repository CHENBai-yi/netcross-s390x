# Architecture Benchmark & Telemetry Report

This report documents actual runtime benchmarks collected natively on **IBM LinuxONE III / z15 hardware (Machine Type: 9175)** running Ubuntu Linux on the `s390x` architecture.

## 1. Mainframe Hardware Environment

| Property | Value |
| :--- | :--- |
| **Processor Architecture** | IBM z/Architecture (`s390x`) |
| **Endianness** | Native Big-Endian (MSB) |
| **Machine Type** | 9175 (IBM LinuxONE III / z15 Express) |
| **vCPU Cores** | 2 |
| **Acceleration Facilities** | `zarch`, `nnpa` (Neural Network Processing Assist), `msa` (Message-Security-Assist Crypto Acceleration), `vx` (Vector Extensions) |

---

## 2. Real-World Network & Handshake Telemetry

Real-time latency metrics collected from `netcross-s390x` probing public global anycast DNS and web infrastructure:

| Target Endpoint | Network Protocol | TCP Handshake Latency | TLS 1.3 Round-Trip | Status |
| :--- | :--- | :--- | :--- | :---: |
| `1.1.1.1:443` (Cloudflare) | TCP / TLS 1.3 | **8.63 ms** | 119.51 ms | HTTP 200 |
| `8.8.8.8:443` (Google DNS) | TCP / TLS 1.3 | **11.19 ms** | 119.04 ms | HTTP 200 |
| `9.9.9.9:443` (Quad9) | TCP / TLS 1.3 | **11.93 ms** | 50.90 ms | HTTP 505 |

---

## 3. Byte-Order Memory Safety Results

```text
=== RUN   TestFloat64Serialization
--- PASS: TestFloat64Serialization (0.00s)
=== RUN   TestInfo
    check_test.go:14: Endian info: Arch: linux/s390x | Endianness: Big-Endian (BE - IBM s390x native)
--- PASS: TestInfo (0.00s)
=== RUN   TestExporterHandler
--- PASS: TestExporterHandler (0.14s)
```

The memory pointer casting and IEEE 754 binary serialization tests confirm zero data inversion and strict alignment on `s390x` hardware.
