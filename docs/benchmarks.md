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

Metrics from `netcross-s390x` probing public global anycast DNS and web
infrastructure, 30 requests per target at concurrency 6. Percentiles are
nearest-rank over the successful requests; every value below is a latency that
was actually observed.

| Target Endpoint | P50 | P90 | P99 | Throughput | Status |
| :--- | ---: | ---: | ---: | ---: | :---: |
| `1.1.1.1:443` (Cloudflare) | **80.56 ms** | 96.69 ms | 105.33 ms | 68.2 req/s | HTTP 200 |
| `8.8.8.8:443` (Google DNS) | **81.62 ms** | 92.39 ms | 106.45 ms | 68.7 req/s | HTTP 200 |
| `9.9.9.9:443` (Quad9) | **38.57 ms** | 40.51 ms | 65.86 ms | 135.3 req/s | HTTP 404 |

### Phase breakdown (p50, milliseconds)

| Target | TCP | TLS | TTFB | OTHER | Total |
| :--- | ---: | ---: | ---: | ---: | ---: |
| `1.1.1.1:443` | 8.36 | 10.21 | 24.59 | 35.13 | 80.56 |
| `8.8.8.8:443` | 9.86 | 17.54 | 10.84 | 41.46 | 81.62 |
| `9.9.9.9:443` | 12.18 | 13.63 | 12.36 | 0.05 | 38.57 |

`OTHER` is the residual between the total and the four measured phases — the
request write, the HTTP/2 `SETTINGS` exchange and scheduler time. It is reported
rather than folded into TTFB so the columns remain honest about what was and was
not measured.

Quad9 is the fastest of the three end-to-end despite having the *slowest* TCP
connect (12.18 ms) and TLS handshake (13.63 ms). The entire difference is in the
residual: 0.05 ms against Cloudflare's 35.13 ms, which is the HTTP/2 connection
setup cost paid on the first request to a fresh connection.

### HTTP/2 requirement

The prober negotiates HTTP/2. Quad9's `9.9.9.9` answers an HTTP/1.1 request with
`505 HTTP Version Not Supported` and speaks only h2, so an HTTP/1.1-only client
reports a healthy anycast resolver as failed:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' --http1.1 https://9.9.9.9
505
$ curl -s -o /dev/null -w '%{http_code}\n' --http2   https://9.9.9.9
404
```

(404 is Quad9's response to `/`; it is a reachable, working endpoint.)

---

## 3. Address-Family Capability

| Property | Value |
| :--- | :--- |
| **IPv4** | 1 non-loopback address (`148.100.76.209`) |
| **IPv6 stack** | enabled — 2 addresses bound |
| **IPv6 global scope** | **none** — `fe80::a1:41ff:feb2:303a` on `enc1000` is link-local, `::1` is loopback |
| **Preferred family** | `ipv4` |

`netcross_ipv6_enabled` is 1 and `netcross_ipv6_available` is 0. The distinction
matters: a check that only asks "is there an IPv6 address?" sees the link-local,
concludes v6 works, and then every probe stalls through the happy-eyeballs
timeout before falling back to IPv4. `-ipv6` on this host correctly fails rather
than silently succeeding over v4.

---

## 4. Byte-Order Correctness Results

The wire-format tests assert against literal byte vectors rather than only
round-tripping, because a round-trip test passes on a little-endian host that
reads back its own mistakes — which is the exact bug the codec exists to prevent.

```text
$ go test ./pkg/endian/ -run 'BigEndian|KnownBytes|Checksum' -v
--- PASS: TestPutUint16IsBigEndianOnTheWire
--- PASS: TestPutUint32IsBigEndianOnTheWire
--- PASS: TestPutUint64IsBigEndianOnTheWire
--- PASS: TestPutFloat64IsBigEndianIEEE754
--- PASS: TestPutFloat32IsBigEndianIEEE754
--- PASS: TestFrameEncodeMatchesKnownBytes
--- PASS: TestFrameDecodeKnownBytes
--- PASS: TestChecksumValueIsStable
--- PASS: TestFrameChecksumDetectsPayloadCorruption
--- PASS: TestFrameChecksumDetectsHeaderCorruption
--- PASS: TestTLVEncodeMatchesKnownBytes
ok  	github.com/CHENBai-yi/netcross-s390x/pkg/endian	0.002s
```

A frame captured on this guest decodes field-for-field as documented in the
README, and the CRC-32C vector (`f696be2f`) is computed independently of the
package so a wrong polynomial or a wrong covered range would fail the test.

### Codec throughput

```text
$ go test ./pkg/endian/ -run=XXX -bench=. -benchtime=200x
BenchmarkFrameEncode-2      200      127.9 ns/op    4002.50 MB/s
BenchmarkFrameDecode-2      200      201.1 ns/op    2546.31 MB/s
```

Frame encode sustains ~4 GB/s on a single s390x vCPU including the CRC-32C.

---

## 5. Test Suite

123 tests across five packages:

| Package | Tests | Covers |
| :--- | ---: | :--- |
| `pkg/endian` | 55 | byte-order vectors, cursor codec, frame/TLV encode-decode, malformed input |
| `pkg/netbench` | 41 | worker pool, phase timings, percentiles, IPv6, error classification |
| `pkg/exporter` | 16 | metric surface, backwards compatibility, HELP/TYPE pairing, concurrency |
| `pkg/sysinfo` | 10 | `/proc/net/if_inet6` parsing, dual-stack detection, hardware profile |
| `pkg/jitter` | 1 | RFC 3550 jitter and MOS estimation |
