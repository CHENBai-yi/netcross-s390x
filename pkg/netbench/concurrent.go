package netbench

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// PhaseTiming breaks a single request into the three stages a client actually
// pays for. Keeping them apart is the whole point of the tool: on a LinuxONE
// guest behind a cloud load balancer, a 40 ms HTTP round trip is usually 1 ms
// of TCP, 2 ms of TLS and 37 ms of the origin thinking, and averaging them
// into one number hides which one to fix.
type PhaseTiming struct {
	// DNSResolve is the name lookup duration. It is zero when the target was
	// dialled by IP literal, which is the common case for this prober.
	DNSResolve time.Duration `json:"dns_resolve"`
	// TCPConnect covers the SYN/SYN-ACK exchange only.
	TCPConnect time.Duration `json:"tcp_connect"`
	// TLSHandshake covers the full handshake through Finished, including the
	// session-ticket flight when the server sends one inline.
	TLSHandshake time.Duration `json:"tls_handshake"`
	// TTFB is the time from the completed handshake to the first response
	// byte, i.e. the origin's think time plus one network leg.
	TTFB time.Duration `json:"ttfb"`
	// Total is the wall time of the whole request.
	Total time.Duration `json:"total"`
}

// Other is the part of Total not covered by the four measured phases: the
// request write, the HTTP/2 SETTINGS exchange when the server negotiates h2,
// and scheduler time. It is reported rather than folded into TTFB because an
// unexplained gap that grows is itself a signal (a stalled SETTINGS ack, a
// slow proxy), and a phase table whose columns do not add up to its total is
// worse than one with an honest remainder.
func (t PhaseTiming) Other() time.Duration {
	sum := t.DNSResolve + t.TCPConnect + t.TLSHandshake + t.TTFB
	if sum >= t.Total {
		return 0
	}
	return t.Total - sum
}

// Add accumulates o into t, which is how per-worker timings are merged.
func (t *PhaseTiming) Add(o PhaseTiming) {
	t.DNSResolve += o.DNSResolve
	t.TCPConnect += o.TCPConnect
	t.TLSHandshake += o.TLSHandshake
	t.TTFB += o.TTFB
	t.Total += o.Total
}

// Div returns t scaled by 1/n, used to turn sums into means.
func (t PhaseTiming) Div(n int) PhaseTiming {
	if n <= 0 {
		return PhaseTiming{}
	}
	d := time.Duration(n)
	return PhaseTiming{
		DNSResolve:   t.DNSResolve / d,
		TCPConnect:   t.TCPConnect / d,
		TLSHandshake: t.TLSHandshake / d,
		TTFB:         t.TTFB / d,
		Total:        t.Total / d,
	}
}

// Millis renders the phases as a compact one-line summary.
func (t PhaseTiming) Millis() string {
	return fmt.Sprintf("dns=%.2fms tcp=%.2fms tls=%.2fms ttfb=%.2fms total=%.2fms",
		ms(t.DNSResolve), ms(t.TCPConnect), ms(t.TLSHandshake), ms(t.TTFB), ms(t.Total))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// Sample is one completed request in a concurrent run.
type Sample struct {
	Seq     int
	OK      bool
	Status  int
	Phases  PhaseTiming
	Err     string
	Started time.Time
}

// Percentiles is the latency distribution of a run, in milliseconds.
//
// P50/P90/P99 rather than mean+stddev because network latency is
// right-skewed: a single 500 ms retransmit moves the mean by 5 ms across 100
// requests but is the only number a user of the service feels. The tail is
// also where s390x guests behave differently from x86 hosts, since a vCPU
// steal on a shared LPAR shows up as tail latency long before it shows up in
// the average.
type Percentiles struct {
	Count int     `json:"count"`
	Min   float64 `json:"min_ms"`
	P50   float64 `json:"p50_ms"`
	P90   float64 `json:"p90_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
	Mean  float64 `json:"mean_ms"`
}

// String renders the distribution as a table row.
func (p Percentiles) String() string {
	return fmt.Sprintf("n=%-4d min=%7.2f p50=%7.2f p90=%7.2f p99=%7.2f max=%7.2f mean=%7.2f",
		p.Count, p.Min, p.P50, p.P90, p.P99, p.Max, p.Mean)
}

// Percentile computes the distribution of samples (milliseconds). It uses the
// nearest-rank method on a sorted copy: for n samples, rank k = ceil(p/100*n),
// clamped to [1,n]. Nearest-rank is used rather than linear interpolation
// because every returned value is then a latency that was actually observed,
// which matters when the tail is what is being reported.
func Percentile(samples []float64) Percentiles {
	if len(samples) == 0 {
		return Percentiles{}
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	n := len(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	return Percentiles{
		Count: n,
		Min:   sorted[0],
		P50:   rank(sorted, 50),
		P90:   rank(sorted, 90),
		P99:   rank(sorted, 99),
		Max:   sorted[n-1],
		Mean:  sum / float64(n),
	}
}

func rank(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	k := int((p/100)*float64(n) + 0.999999) // ceil
	if k < 1 {
		k = 1
	}
	if k > n {
		k = n
	}
	return sorted[k-1]
}

// ConcurrentResult is the aggregate of a concurrent run against one target.
type ConcurrentResult struct {
	Target      string        `json:"target"`
	Concurrency int           `json:"concurrency"`
	Requests    int           `json:"requests"`
	Succeeded   int           `json:"succeeded"`
	Failed      int           `json:"failed"`
	Duration    time.Duration `json:"duration_ns"`
	RPS         float64       `json:"requests_per_second"`
	SuccessRate float64       `json:"success_rate"`
	Latency     Percentiles   `json:"latency"`
	// DNSMean is the mean name-resolution time, zero for IP-literal targets.
	DNSMean float64     `json:"dns_resolve_mean_ms"`
	TCP     Percentiles `json:"tcp_connect"`
	TLS     Percentiles `json:"tls_handshake"`
	TTFB    Percentiles `json:"ttfb"`
	// Other is the mean residual between Total and the four measured phases.
	OtherMean float64 `json:"other_mean_ms"`
	// OtherP is the residual distribution, so a growing tail in the
	// unexplained portion is visible rather than averaged away.
	Other  Percentiles    `json:"other"`
	Errors map[string]int `json:"errors,omitempty"`
}

// Config controls a concurrent benchmark run.
type Config struct {
	// Requests is the total number of requests to issue. Ignored when
	// Duration is non-zero.
	Requests int
	// Duration runs for a wall-clock window instead of a fixed count.
	Duration time.Duration
	// Concurrency is the number of in-flight requests, i.e. the worker pool
	// size. Values below 1 are clamped to 1.
	Concurrency int
	// Timeout bounds a single request.
	Timeout time.Duration
	// InsecureSkipVerify disables certificate verification. It exists for
	// probing appliances with self-signed certs and defaults to false.
	InsecureSkipVerify bool
	// Network forces the address family: "tcp4", "tcp6" or "" for both.
	Network string
}

func (c Config) normalize() Config {
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.Requests <= 0 && c.Duration <= 0 {
		c.Requests = 10
	}
	return c
}

// RunConcurrent issues cfg.Requests requests (or as many as fit in
// cfg.Duration) against url using a fixed pool of cfg.Concurrency workers.
//
// The pool is fixed rather than "one goroutine per request" so that the
// reported req/s is a property of the target and not of how many sockets the
// prober was willing to open. A fixed pool also bounds memory on the 2-core
// guest this is developed on, where an unbounded fan-out of 10 000 goroutines
// would spend its time in the scheduler instead of on the network.
func RunConcurrent(ctx context.Context, url string, cfg Config) ConcurrentResult {
	cfg = cfg.normalize()
	res := ConcurrentResult{
		Target:      url,
		Concurrency: cfg.Concurrency,
		Errors:      map[string]int{},
	}

	client := newProbeClient(cfg)

	var (
		mu      sync.Mutex
		samples []Sample
		seq     int64
	)
	deadline := time.Time{}
	if cfg.Duration > 0 {
		deadline = time.Now().Add(cfg.Duration)
	}

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				if !deadline.IsZero() && time.Now().After(deadline) {
					return
				}
				n := atomic.AddInt64(&seq, 1)
				if cfg.Duration == 0 && int(n) > cfg.Requests {
					return
				}
				s := doRequest(ctx, client, url, int(n), cfg.Timeout)
				mu.Lock()
				samples = append(samples, s)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	res.Duration = time.Since(start)

	aggregate(&res, samples)
	if res.Duration > 0 {
		res.RPS = float64(len(samples)) / res.Duration.Seconds()
	}
	if len(samples) > 0 {
		res.SuccessRate = float64(res.Succeeded) / float64(len(samples)) * 100
	}
	return res
}

func newProbeClient(cfg Config) *http.Client {
	dialer := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	network := cfg.Network
	if network == "" {
		network = "tcp"
	}
	return &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify},
			TLSHandshakeTimeout: cfg.Timeout,
			// A fresh connection per request is deliberate: with keep-alive
			// the second and later requests skip TCP and TLS entirely, so
			// the phase breakdown would report zero for the two stages the
			// tool exists to measure.
			DisableKeepAlives:   true,
			MaxIdleConnsPerHost: cfg.Concurrency,
			// HTTP/2 must be negotiable: Quad9's 9.9.9.9 answers a HTTP/1.1
			// request with 505 and only speaks h2, so forcing HTTP/1.1 would
			// report a healthy anycast resolver as a failed probe.
			ForceAttemptHTTP2: true,
		},
	}
}

func doRequest(ctx context.Context, client *http.Client, url string, seq int, timeout time.Duration) Sample {
	s := Sample{Seq: seq, Started: time.Now()}

	var (
		t0       = time.Now()
		dnsStart time.Time
		tcpStart time.Time
		tlsStart time.Time
		// connReady marks the moment the connection is usable, i.e. after
		// the TLS handshake when there is one. TTFB is measured from here so
		// the four phases partition the request instead of overlapping: for
		// a TLS target, httptrace fires GotConn *before* the handshake, so
		// measuring TTFB from GotConn would silently count the handshake
		// twice and make the columns of the phase table non-additive.
		connReady time.Time
	)

	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				s.Phases.DNSResolve = time.Since(dnsStart)
			}
		},
		ConnectStart: func(_, _ string) { tcpStart = time.Now() },
		ConnectDone: func(_, _ string, err error) {
			if err == nil && !tcpStart.IsZero() {
				s.Phases.TCPConnect = time.Since(tcpStart)
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if !tlsStart.IsZero() {
				s.Phases.TLSHandshake = time.Since(tlsStart)
			}
			if err == nil {
				connReady = time.Now()
			}
		},
		GotConn: func(httptrace.GotConnInfo) {
			// Plaintext only: for TLS, TLSHandshakeDone overwrites this with
			// the later, correct instant.
			if connReady.IsZero() {
				connReady = time.Now()
			}
		},
		GotFirstResponseByte: func() {
			if !connReady.IsZero() {
				s.Phases.TTFB = time.Since(connReady)
			} else {
				s.Phases.TTFB = time.Since(t0)
			}
		},
	}

	reqCtx, cancel := context.WithTimeout(httptrace.WithClientTrace(ctx, trace), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		s.Err = "build request: " + err.Error()
		s.Phases.Total = time.Since(t0)
		return s
	}
	req.Header.Set("User-Agent", "netcross-s390x/probe")

	resp, err := client.Do(req)
	s.Phases.Total = time.Since(t0)
	if err != nil {
		s.Err = classifyError(err)
		return s
	}
	// Drain a bounded amount so the connection can be reused by the transport
	// even with keep-alives off, and so a huge body does not dominate the
	// measurement.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	resp.Body.Close()

	s.Status = resp.StatusCode
	s.OK = resp.StatusCode >= 200 && resp.StatusCode < 500
	if !s.OK {
		s.Err = fmt.Sprintf("http_status_%d", resp.StatusCode)
	}
	return s
}

// classifyError collapses the noisy error strings Go produces into stable
// buckets, so the error histogram in the report has a handful of keys instead
// of one key per distinct IP:port.
func classifyError(err error) string {
	var nerr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &nerr) && nerr.Timeout():
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		switch opErr.Op {
		case "dial":
			return "connect_refused_or_unreachable"
		case "read":
			return "read_error"
		case "write":
			return "write_error"
		}
	}
	msg := err.Error()
	switch {
	case containsAny(msg, "certificate", "x509"):
		return "tls_certificate"
	case containsAny(msg, "handshake"):
		return "tls_handshake"
	case containsAny(msg, "EOF"):
		return "eof"
	}
	return "other"
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) <= len(s) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func aggregate(res *ConcurrentResult, samples []Sample) {
	if len(samples) == 0 {
		return
	}
	res.Requests = len(samples)
	total := make([]float64, 0, len(samples))
	var dnsSum float64
	tcp := make([]float64, 0, len(samples))
	tlsv := make([]float64, 0, len(samples))
	ttfb := make([]float64, 0, len(samples))
	other := make([]float64, 0, len(samples))

	for _, s := range samples {
		if s.OK {
			res.Succeeded++
			dnsSum += ms(s.Phases.DNSResolve)
			total = append(total, ms(s.Phases.Total))
			tcp = append(tcp, ms(s.Phases.TCPConnect))
			tlsv = append(tlsv, ms(s.Phases.TLSHandshake))
			ttfb = append(ttfb, ms(s.Phases.TTFB))
			other = append(other, ms(s.Phases.Other()))
		} else {
			res.Failed++
			key := s.Err
			if key == "" {
				key = "unknown"
			}
			res.Errors[key]++
		}
	}
	if res.Succeeded > 0 {
		res.DNSMean = dnsSum / float64(res.Succeeded)
	}
	res.Latency = Percentile(total)
	res.TCP = Percentile(tcp)
	res.TLS = Percentile(tlsv)
	res.TTFB = Percentile(ttfb)
	res.Other = Percentile(other)
	res.OtherMean = res.Other.Mean
}

// RunMultiTarget runs RunConcurrent against several targets sequentially and
// returns one result per target. Targets are run one after another rather than
// in parallel so that one target's load does not inflate another's latency
// measurement on a 2-vCPU host.
func RunMultiTarget(ctx context.Context, targets []string, cfg Config) []ConcurrentResult {
	out := make([]ConcurrentResult, 0, len(targets))
	for _, t := range targets {
		out = append(out, RunConcurrent(ctx, t, cfg))
	}
	return out
}

// AggregateTotal sums several per-target results into a single view, which is
// what the Prometheus exporter reports for the "all targets" series.
func AggregateTotal(results []ConcurrentResult) ConcurrentResult {
	total := ConcurrentResult{Target: "all", Errors: map[string]int{}}
	var dur time.Duration
	for _, r := range results {
		total.Requests += r.Requests
		total.Succeeded += r.Succeeded
		total.Failed += r.Failed
		total.RPS += r.RPS
		dur += r.Duration
		for k, v := range r.Errors {
			total.Errors[k] += v
		}
	}
	total.Duration = dur
	if total.Requests > 0 {
		total.SuccessRate = float64(total.Succeeded) / float64(total.Requests) * 100
	}
	return total
}
