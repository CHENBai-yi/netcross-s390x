# NetCross-s390x 🚀

[![Multi-Arch Build & Test CI](https://github.com/CHENBai-yi/netcross-s390x/actions/workflows/ci.yml/badge.svg)](https://github.com/CHENBai-yi/netcross-s390x/actions/workflows/ci.yml)
[![Go Report Card](https://img.shields.io/badge/Go-1.22+-blue.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Architecture](https://img.shields.io/badge/Architecture-IBM%20s390x%20(Big--Endian)-purple.svg)]()
[![Prometheus Ready](https://img.shields.io/badge/Prometheus-Metrics%20Exporter-orange.svg)]()
[![Dependencies](https://img.shields.io/badge/dependencies-0-brightgreen.svg)]()

**NetCross-s390x** is a network diagnostics engine, concurrent latency prober and
Prometheus exporter written for **IBM Z and LinuxONE (`s390x`)** mainframes. It
covers two things that are awkward specifically on a big-endian host: measuring
network behaviour without host byte order leaking into the numbers, and
serialising structured telemetry into a wire format that is byte-identical on
`s390x`, `amd64` and `arm64`.

The project is standard-library only — no third-party modules, no `go.sum`.

---

## 🌟 Key Features

- **Big-Endian wire codec** — a cursor-based serialiser for `uint8/16/32/64`,
  `int`, `float32/64`, `time.Duration`, fixed-width padding and length-prefixed
  byte strings, plus a 20-byte checksummed frame header and a TLV record layer.
  Every scalar routes through `encoding/binary.BigEndian` rather than an
  `unsafe` cast, so frames produced on `s390x` parse on `amd64` without a swap
  and vice versa.
- **Concurrent benchmarking with percentiles** — a fixed worker pool with
  nearest-rank P50/P90/P99 over end-to-end latency *and* over each protocol phase
  (DNS, TCP connect, TLS handshake, TTFB), plus throughput and a bucketed error
  histogram.
- **IPv6 dual-stack awareness** — explicit `tcp4`/`tcp6` selection, bracketed
  literal handling, per-target family reachability, and a happy-eyeballs penalty
  measurement. Address-family capability is read from `/proc/net/if_inet6`
  directly, so "IPv6 is enabled" and "IPv6 can actually reach a peer" are
  reported as two different facts.
- **Prometheus exporter** — 39 metric families covering latency percentiles,
  phase timings and their tails, phase share ratios, throughput, concurrency,
  dual-stack state and error reasons. The v0.2 series names are still emitted as
  aliases.
- **Foreign byte-order diagnosis** — a received frame that fails to decode is
  audited under both byte orders: the magic word and the payload length act as
  two independent witnesses, so a peer that serialised with host byte order is
  named as such instead of being reported as `bad magic`. Works on every
  architecture because host order is derived from a runtime probe, not assumed.
- **Mainframe hardware discovery** — parses `/proc/cpuinfo` for the machine type
  (`9175` on the guest this is developed on), the named feature list
  (`zarch msa vx vxe2 nnpa …`) and the numeric STFLE **facility bitmap**, which
  is decoded into a `FacilitySet` so `HasVector()`, `HasNNPA()`, `HasSIE()` and
  friends answer "will this instruction trap here" rather than "what string did
  the kernel print".
- **CI on three architectures** — `linux/amd64`, `linux/arm64`, `linux/s390x`
  builds plus a `gofmt` gate, `go vet` and a `-race` test run.

---

## 📋 Architecture & Data Flow

```
+------------------------------------------------------------------------+
|                         NetCross-s390x Engine                          |
+-----------------------------------+------------------------------------+
                                    |
            +-----------------------+-----------------------+
            |                                               |
   [pkg/endian]                                    [pkg/netbench]
   buffer.go   cursor codec                        bench.go     serial probe
   wire.go     frame + CRC-32C + TLV               concurrent.go worker pool,
   check.go    runtime byte-order probe                         phase timings,
                                                                percentiles
            |                                               |
            |                                       [pkg/sysinfo]
            |                                       s390x.go     machine type
            |                                       dualstack.go /proc/net/if_inet6
            |                                               |
            +-----------------------+-----------------------+
                                    |
                    +---------------+---------------+
                    |                               |
             [CLI / -format]                [Prometheus Daemon]
             text | json | prometheus       serves :9100/metrics
```

---

## 🛠️ Usage Guide

### 1. Run one-shot diagnostics (CLI)

```bash
./netcross
```

### 2. Concurrent load with percentile tables

```bash
./netcross -requests 50 -concurrency 8
```

### 3. Structured JSON report

```bash
./netcross -format json
./netcross -json                 # v0.2 spelling, still supported
```

### 4. Prometheus exposition to stdout

```bash
./netcross -format prometheus
```

### 5. Launch the exporter daemon

```bash
./netcross -listen :9100 -interval 15 -concurrency 4 -requests 10
curl -s http://localhost:9100/metrics | head -40
```

### 6. Force IPv6, or probe your own targets

```bash
./netcross -ipv6 -targets '[2606:4700:4700::1111]:443'
./netcross -targets one.one.one.one,dns.google,1.1.1.1:443
```

### Flag reference

| Flag | Default | Meaning |
|---|---|---|
| `-json` | `false` | Shorthand for `-format json` (v0.2 compatible) |
| `-format` | `text` | `text`, `json` or `prometheus` |
| `-timeout` | `5` | Per-request timeout, seconds |
| `-requests` | `10` | Requests per target |
| `-concurrency` | `1` | In-flight requests per target |
| `-ipv6` | `false` | Pin probes to `tcp6`; never falls back to IPv4 |
| `-targets` | built-in set | Comma-separated `host:port` or URLs |
| `-insecure` | `false` | Skip TLS verification |
| `-listen` | `""` | Serve metrics on this address instead of probing once |
| `-interval` | `15` | Exporter poll period, seconds |
| `-version` | `false` | Print version and exit |

---

## 🔢 Big-Endian Wire Format

`pkg/endian` exists because the obvious way to serialise a `uint32` on `s390x`
is wrong everywhere else. On this host

```go
b := make([]byte, 4)
*(*uint32)(unsafe.Pointer(&b[0])) = v   // correct on s390x, corrupts on amd64
```

compiles, runs, and produces the right bytes — and the identical code produces
reversed bytes on every x86 or ARM peer. Round-trip tests cannot catch this,
because a little-endian writer paired with a little-endian reader is
self-consistent and simply wrong. `Buffer` therefore routes every scalar through
`encoding/binary.BigEndian`, which the compiler turns into a no-op on `s390x` and
a single `BSWAP` elsewhere.

### The frame header

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                          magic "NCRS"                         |  0-3
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  version      |  flags        |          msg type             |  4-7
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        sequence number                        |  8-11
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        payload length                         | 12-15
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       CRC-32C checksum                        | 16-19
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                            payload ...                        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

The checksum is CRC-32C (Castagnoli) over the **first 16 header bytes plus the
payload**. Covering the header means a flipped flag or message-type bit is caught
by the same check that protects the body, so a corrupted `msg type` cannot route
a payload to the wrong handler. `magic` is a four-byte sync word rather than a
length-only prefix so a peer scanning a stream for a frame boundary cannot
false-positive on a run of zeroes.

### A real frame, byte for byte

A `MsgProbeResp` carrying three TLV records — target `1.1.1.1:443`, latency
`39030000 ns`, status `200` — encoded on the s390x guest:

```
0000  4e 43 52 53 01 01 00 02 00 00 00 01 00 00 00 21
0010  27 1d a3 f9 00 01 00 0b 31 2e 31 2e 31 2e 31 3a
0020  34 34 33 00 02 00 08 00 00 00 00 02 53 8c f0 00
0030  03 00 02 00 c8
```

| Offset | Bytes | Field | Value |
|---|---|---|---|
| `00` | `4e 43 52 53` | magic | `"NCRS"` |
| `04` | `01` | version | 1 |
| `05` | `01` | flags | checksum present |
| `06` | `00 02` | msg type | `probe_response` |
| `08` | `00 00 00 01` | sequence | 1 |
| `0c` | `00 00 00 21` | payload length | 33 |
| `10` | `27 1d a3 f9` | CRC-32C | over bytes `00`–`0f` + payload |
| `14` | `00 01 00 0b` | TLV tag 1, len 11 | target |
| `18` | `31 2e … 33` | value | `1.1.1.1:443` |
| `23` | `00 02 00 08` | TLV tag 2, len 8 | latency |
| `27` | `00 00 00 00 02 53 8c f0` | value | 39030000 ns |
| `2f` | `00 03 00 02 00 c8` | TLV tag 3, len 2 | status 200 |

Read the length field at offset `0c`: `00 00 00 21` is 33, and exactly 33 bytes
of payload follow — big-endian, readable straight out of a hex dump without
knowing which host captured it.

### API sketch

```go
w := endian.NewWriter(64)
w.PutUint32(0xDEADBEEF)              //  de ad be ef
w.PutFloat64(1.0)                    //  3f f0 00 00 00 00 00 00
w.PutString("linuxone")              //  00 08 + UTF-8
w.PutFixedBytes(nonce[:], 16)        //  right-padded with zeroes
if err := w.Err(); err != nil { ... } // no panics: errors are latched

r := endian.NewBuffer(w.Bytes())
v := r.Uint32()
```

Bad input never panics. A read past the end latches `ErrShortBuffer` and leaves
the cursor where it was; a `uint32` length prefix is checked against
`MaxVarintPayload` (16 MiB) *before* allocating, so a hostile `0xFFFFFFFF`
prefix returns `ErrLengthOverflow` instead of attempting a 4 GiB `make()` that
would OOM-kill a 2-vCPU guest.

---

## ⚡ Concurrent Benchmarking

`pkg/netbench` runs a **fixed-size worker pool** per target rather than one
goroutine per request. That choice is what makes the reported `req/s` a property
of the target instead of a property of how many sockets the prober was willing
to open, and it bounds memory on the 2-vCPU host this is developed on.

### Measured throughput vs. concurrency

40 requests against `1.1.1.1:443` from the ci15 guest (LinuxONE III, machine type
9175, 2 vCPU), via `./netcross -requests 40 -concurrency N`:

| Concurrency | P50 (ms) | P90 (ms) | P99 (ms) | req/s |
|---:|---:|---:|---:|---:|
| 1 | 83.66 | 89.65 | 131.75 | 11.7 |
| 2 | 80.88 | 89.08 | 131.57 | 23.6 |
| 4 | 85.33 | 91.91 | 119.06 | 43.7 |
| 8 | 92.02 | 105.34 | 199.52 | 76.8 |
| 16 | 86.88 | 96.28 | 123.72 | 147.4 |

Throughput scales close to linearly to 16 in-flight requests while P50 stays
flat within ~10%: the round trip is dominated by the ~80 ms path to the anycast
edge, not by the guest. P99 is the column that moves — at concurrency 8 it hits
199 ms, roughly 2.4× the median, which is the shape you expect when a single
request loses a scheduling race on a shared LPAR.

### Percentiles, not means

Latency distributions on a network are right-skewed. A single 500 ms retransmit
across 100 requests moves the mean by 5 ms but is the only number a user of the
service actually feels. `Percentile` uses the **nearest-rank** method
(`k = ceil(p/100 · n)`, clamped to `[1,n]`), so every reported value is a latency
that was really observed rather than an interpolation between two of them —
which matters precisely when the tail is what you are reporting.

### Phase breakdown

`./netcross -requests 50 -concurrency 8` against the three default anycast
resolvers:

```
TARGET               OK/REQ   P50 ms   P90 ms   P99 ms      req/s
cloudflare-dns        50/50    85.88   100.10   116.27       83.0
google-dns            50/50    83.07    94.73   111.74       85.5
quad9-dns             50/50    39.03    43.29    66.97      183.5

Phase breakdown (p50, milliseconds)
  TARGET                  TCP        TLS       TTFB      OTHER      TOTAL
  cloudflare-dns         8.40      10.41      25.29      36.94      85.88
  google-dns             9.95      17.57      11.17      42.49      83.07
  quad9-dns             12.25      13.70      12.42       0.04      39.03
```

`OTHER` is the residual between the total and the four measured phases — the
request write, the HTTP/2 `SETTINGS` exchange when the server negotiates h2, and
scheduler time. It is reported rather than folded into TTFB because a phase
table whose columns do not add up to its total is worse than one with an honest
remainder, and a growing residual is itself a signal (a stalled `SETTINGS` ack, a
slow intermediary).

Two things this table shows that a single averaged number would hide:

- Quad9's edge answers in 39 ms while Cloudflare's takes 86 ms from this host,
  but the *handshake* is not the difference — Quad9's TCP (12.25 ms) and TLS
  (13.70 ms) are the slowest of the three. The gap is Cloudflare's 25 ms TTFB
  plus a 37 ms residual.
- Google's TLS handshake is 17.6 ms against Quad9's 13.7 ms for a similar
  ~10–12 ms RTT, i.e. roughly one extra round trip, consistent with a session
  ticket not being reused on a fresh connection.

### A note on HTTP/2

The probe client negotiates HTTP/2. This is not cosmetic: Quad9's `9.9.9.9`
answers an HTTP/1.1 request with `505 HTTP Version Not Supported` and speaks only
h2, so forcing HTTP/1.1 reported a perfectly healthy anycast resolver as
`0/20 failed`. Verifiable by hand:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' --http1.1 https://9.9.9.9
505
$ curl -s -o /dev/null -w '%{http_code}\n' --http2   https://9.9.9.9
404
```

---

## 🌐 IPv6 Dual-Stack Support

IPv6 breaks two assumptions the old prober made: that a target is `host:port`
with a dot in it, and that "the host has an IPv6 address" means "the host can
reach an IPv6 peer".

### Literal handling

```go
netbench.NormalizeHostPort("2606:4700::1111", 443)  // "[2606:4700::1111]:443"
netbench.FamilyOf("[::1]:9100")                     // FamilyIPv6
netbench.NetworkFor(netbench.FamilyIPv6)            // "tcp6"
```

A bare IPv6 literal needs brackets before it can carry a port, so
`NormalizeHostPort` adds them and supplies a default port. `FamilyOf` classifies
without any I/O, and reports a hostname as `FamilyNone` because the answer
depends on what DNS returns and which address happy-eyeballs picks.

### Enabled vs. usable

`sysinfo.DetectDualStack` parses `/proc/net/if_inet6` directly — no `iproute2`
dependency, because the exporter runs unprivileged — and separates two facts:

```console
$ ./netcross -requests 1 | grep Addressing
Addressing        : ipv4=1 addr | ipv6=local-only(2 iface) | preferred=ipv4
```

This guest has two IPv6 addresses bound — `::1` on `lo` and a `fe80::` link-local
on `enc1000` — so `netcross_ipv6_enabled` is 1. It has no global-scope address,
so `netcross_ipv6_available` is 0. A naive `net.InterfaceAddrs()`-based check
would see the link-local, conclude IPv6 works, and then every probe would stall
through the happy-eyeballs timeout before falling back to IPv4. Splitting the two
metrics is what lets a dashboard distinguish "no v6 stack" from "v6 stack, no
route".

`-ipv6` pins the family and refuses to fall back:

```console
$ ./netcross -ipv6 -requests 2 -targets 1.1.1.1:443
Address family    : tcp6 (forced)
TARGET               OK/REQ   P50 ms   P90 ms   P99 ms      req/s
1.1.1.1:443             0/2     0.00     0.00     0.00    20079.7
```

Zero successes is the correct answer: there is no IPv6 route to that address, and
silently succeeding over IPv4 would make the flag meaningless. The daemon mode
additionally prints a warning at startup when `-ipv6` is set without a global
address.

### Per-target reachability

`ProbeDualStack` resolves both families and times a TCP connect over each,
reporting the family that is actually faster and the penalty paid when IPv6 is
advertised but slower or unusable:

```
Address family reachability
  TARGET             IPv4   IPv6     v4 RTT     v6 RTT
  cloudflare-dns      yes      -    8.40 ms          -
  google-dns          yes      -    9.79 ms          -
  quad9-dns           yes      -   12.03 ms          -
```

---

## 📊 Prometheus Metrics

`netcross -listen :9100` serves `text/plain; version=0.0.4`. 39 metric families:

### System & capability

| Metric | Type | Description |
|---|---|---|
| `netcross_system_info` | gauge | `arch`, `machine_type`, `num_cpu`, `endianness` labels |
| `netcross_ipv6_available` | gauge | 1 when a **global-scope** IPv6 address is bound |
| `netcross_ipv6_enabled` | gauge | 1 when any IPv6 address is bound, including link-local |
| `netcross_ipv6_interfaces` | gauge | IPv6 addresses in this netns |
| `netcross_ipv4_available` | gauge | 1 when a non-loopback IPv4 address is bound |

### Probe counters

| Metric | Type | Description |
|---|---|---|
| `netcross_probes_total` | counter | Requests issued in the last cycle |
| `netcross_probes_successful` | counter | Requests that completed with 2xx–4xx |
| `netcross_probes_failed` | counter | Requests that did not |
| `netcross_poll_cycles_total` | counter | Completed collection cycles since start |
| `netcross_last_collect_timestamp_seconds` | gauge | Unix time of the last cycle |
| `netcross_probe_errors_total{reason}` | counter | Failures by bucket: `timeout`, `dns_error`, `connect_refused_or_unreachable`, `tls_certificate`, `tls_handshake`, `eof`, … |

### Latency percentiles `{target}`

| Metric | Type | Description |
|---|---|---|
| `netcross_probe_latency_p50_seconds` | gauge | Median end-to-end latency |
| `netcross_probe_latency_p90_seconds` | gauge | 90th percentile |
| `netcross_probe_latency_p99_seconds` | gauge | 99th percentile |
| `netcross_probe_latency_max_seconds` | gauge | Slowest observed request |

### Phase timings `{target}`

| Metric | Type | Description |
|---|---|---|
| `netcross_tcp_connect_seconds` | gauge | SYN → SYN-ACK (p50) |
| `netcross_tls_handshake_seconds` | gauge | Handshake through `Finished` (p50) |
| `netcross_http_ttfb_seconds` | gauge | Usable connection → first response byte (p50) |
| `netcross_dns_resolve_seconds` | gauge | Mean name resolution; 0 for IP literals |

### Phase tails `{target}`

| Metric | Type | Description |
|---|---|---|
| `netcross_tcp_connect_p90_seconds` / `_p99_` | gauge | TCP handshake tail |
| `netcross_tls_handshake_p90_seconds` / `_p99_` | gauge | TLS handshake tail |
| `netcross_http_ttfb_p90_seconds` / `_p99_` | gauge | TTFB tail |
| `netcross_phase_measured{phase="tcp\|tls\|ttfb"}` | gauge | 1 when the phase produced a non-zero measurement |
| `netcross_phase_share_ratio{phase="tcp\|tls\|ttfb\|other"}` | gauge | Phase p50 as a fraction of end-to-end p50, clamped to `[0,1]` |

The tail matters more than the median on a shared LPAR: a vCPU steal moves P99
long before it moves P50, so a phase can look healthy at the median while its
tail is dominated by stolen cycles. A phase that was never measured (no TLS on a
plaintext target) is emitted at `0` with `netcross_phase_measured` at `0`, so a
query can tell "not measured" from "measured and genuinely zero".

### Throughput & concurrency `{target}`

| Metric | Type | Description |
|---|---|---|
| `netcross_probe_requests_per_second` | gauge | Sustained throughput during the cycle |
| `netcross_probe_requests` | gauge | Requests issued in the cycle |
| `netcross_probe_concurrency` | gauge | Worker-pool size used |
| `netcross_probe_success_rate` | gauge | Success fraction, 0–1 |
| `netcross_probe_success` | gauge | 1 when the target had zero failures |
| `netcross_probe_error_rate` | gauge | Failed/requests as a fraction, 0 when no requests were issued |
| `netcross_probe_error_ratio_by_reason{reason}` | gauge | Per-target share of requests by classified failure reason |

### Dual-stack `{target}`

| Metric | Type | Description |
|---|---|---|
| `netcross_target_has_ipv4` | gauge | Target resolves to an IPv4 address |
| `netcross_target_has_ipv6` | gauge | Target resolves to an IPv6 address |
| `netcross_happy_eyeballs_penalty_seconds` | gauge | Extra latency when v6 is advertised but slow/unusable |

### Backwards compatibility

`netcross_probe_tcp_connect_seconds` and `netcross_probe_tls_handshake_seconds`
from v0.2 are **still emitted**, carrying the same values as the new
`netcross_tcp_connect_seconds` / `netcross_tls_handshake_seconds`. Renaming a
series silently breaks every dashboard and alert that references it, so the old
names are kept as aliases and a test asserts all seven original series are still
present in the output.

### Example scrape

```text
# HELP netcross_system_info Static hardware and architecture profile
# TYPE netcross_system_info gauge
netcross_system_info{arch="s390x",machine_type="9175",num_cpu="2",endianness="big"} 1

# HELP netcross_ipv6_available 1 when a global-scope IPv6 address is bound and usable for remote probes
# TYPE netcross_ipv6_available gauge
netcross_ipv6_available 0

# HELP netcross_probe_latency_p50_seconds Median end-to-end request latency
# TYPE netcross_probe_latency_p50_seconds gauge
netcross_probe_latency_p50_seconds{target="cloudflare-dns"} 0.085880

# HELP netcross_probe_tcp_connect_seconds TCP handshake duration in seconds
# TYPE netcross_probe_tcp_connect_seconds gauge
netcross_probe_tcp_connect_seconds{target="cloudflare-dns"} 0.008400
```

Every series is emitted with a matching `HELP` and `TYPE` line, which is what
`promtool check metrics` enforces; the exporter's tests parse the output and
verify that pairing rather than trusting it.

---

## 🧪 Testing

```console
$ go test ./... -count=1
ok   github.com/CHENBai-yi/netcross-s390x/pkg/endian     0.015s
ok   github.com/CHENBai-yi/netcross-s390x/pkg/exporter   0.393s
ok   github.com/CHENBai-yi/netcross-s390x/pkg/jitter     0.006s
ok   github.com/CHENBai-yi/netcross-s390x/pkg/netbench   0.474s
ok   github.com/CHENBai-yi/netcross-s390x/pkg/sysinfo    0.001s
```

123 tests across five packages. The `pkg/endian` tests assert against **literal
byte vectors** — including a CRC-32C computed independently of the package — and
not only round-trips, because a round-trip test passes on a little-endian host
that reads back its own mistakes. Network tests use real `httptest` listeners
rather than a mocked `RoundTripper`, since the phase timings come from
`httptrace` callbacks that never fire on a fake transport.

### Micro-benchmarks

```console
$ go test ./pkg/endian/ -run=XXX -bench=. -benchtime=200x
BenchmarkBufferPutUint64-2      200      6358 ns/op
BenchmarkBufferGetUint32-2      200      4118 ns/op
BenchmarkEndianCheck-2          200       235.0 ns/op
BenchmarkFrameEncode-2          200       127.9 ns/op    4002.50 MB/s
BenchmarkFrameDecode-2          200       201.1 ns/op    2546.31 MB/s
```

Frame encode sustains ~4 GB/s on a single s390x vCPU including the CRC-32C,
which is why the checksum is not a throughput concern for a telemetry path.

---

---

## 🏛️ Why this needs a permanent s390x host

This project is developed and validated on an IBM LinuxONE Community Cloud
instance (machine type 9175, Ubuntu 22.04). That instance is a **60-day trial**,
which is the wrong shape for what this tool exists to do.

**What a trial window cannot support:**

1. **Long-horizon network baselines.** Latency and jitter on a mainframe are
   only meaningful across weeks — routing shifts, peer maintenance windows and
   congestion patterns all have multi-day periodicity. A 60-day window that ends
   mid-measurement throws the series away.
2. **Continuous CI.** The value of an s390x CI target comes from running it on
   every upstream change, indefinitely. A CI runner that expires is a one-off
   port, not a pipeline.
3. **Cross-version regression.** Verifying that a big-endian wire format stays
   byte-identical across Go releases requires several toolchains installed
   side by side on the same s390x host, over months.
4. **Acting as a reference implementation.** Other projects porting to s390x need
   a known-good, continuously-running example to diff against.

**What the permanent VM would run:**

- A nightly multi-target network probe with a rolling baseline store
  (`--baseline-check` fails the run when latency regresses beyond tolerance).
- The Prometheus exporter as a long-lived service, scraped continuously, so
  phase-level latency trends are visible instead of sampled once.
- Multi-toolchain builds (Go 1.22 / 1.23 / 1.24) validating that the wire codec
  produces identical bytes on every release.
- Periodic IPv6 dual-stack reachability sweeps.

**Validated on:** IBM LinuxONE III, machine type `9175`, Ubuntu 22.04 LTS,
`s390x` big-endian, Go 1.23.2. Frame encode sustains ~4 GB/s per vCPU including
CRC-32C.

---

## 📄 License

Licensed under the [MIT License](./LICENSE). Contributions and PRs for mainframe
Linux optimizations are warmly welcomed — see [CONTRIBUTING.md](./CONTRIBUTING.md).
