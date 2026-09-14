// Package synth runs synthetic HTTP checks against SaaS, cloud, CDN and API
// endpoints from this machine and records a per-phase timing breakdown for
// each: DNS, TCP connect, TLS handshake, time-to-first-byte, and download.
//
// ICMP tells you whether the internet is reachable; it does not tell you
// whether Teams, GitHub, S3 or an AI API is actually *usable* right now, nor
// which phase of a request is slow. That is what commercial "internet
// performance monitoring" products sell as synthetic / website / API / SaaS /
// CDN monitoring from a global vantage network. Agent Smith cannot offer many
// vantage points, but it can offer the one that matters to the user — their
// own machine — and break each request down so a slow DNS phase, a slow TLS
// handshake, or a slow origin can be told apart. It also reads the CDN edge
// (PoP) that served the request from well-known response headers, so a
// sudden change of edge shows up alongside a latency change.
package synth

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
	"strings"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
)

// userAgent is sent with every request; some CDNs reject Go's default UA.
const userAgent = "AgentSmith/1.0 (+https://github.com/NYBaywatch/agent-smith)"

// maxBody caps how much of a response body is read, so a huge page cannot
// stall the check loop. Reading (part of) the body is what makes the Download
// phase meaningful.
const maxBody = 2 << 20 // 2 MiB

// maxRedirects bounds redirect following per check.
const maxRedirects = 3

// testTLSConfig, when non-nil, overrides the transport's TLS config. It exists
// so tests can accept an httptest TLS server's self-signed certificate.
var testTLSConfig *tls.Config

// Category groups checks by the kind of service they represent.
type Category string

// Built-in categories. CategoryCustom is for user-defined checks.
const (
	CategorySaaS   Category = "SaaS"
	CategoryCloud  Category = "Cloud"
	CategoryCDN    Category = "CDN"
	CategoryAI     Category = "AI API"
	CategoryCustom Category = "Custom"
)

// Check describes one synthetic HTTP check. JSON tags use snake_case so a set
// of checks can be kept in a user config file.
type Check struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Category Category `json:"category"`
	Provider string   `json:"provider,omitempty"` // e.g. "Cloudflare", "AWS"
	Method   string   `json:"method,omitempty"`   // default "GET"
	// ExpectStatus is the status code that counts as success. 0 means any
	// status below 500 is OK: a 401/403/404 from an API still proves the
	// service is reachable and responding.
	ExpectStatus int   `json:"expect_status,omitempty"`
	Enabled      *bool `json:"enabled,omitempty"` // nil = enabled
}

// IsEnabled reports whether the check should run (nil Enabled means yes).
func (c Check) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// method returns the HTTP method to use, defaulting to GET.
func (c Check) method() string {
	if c.Method == "" {
		return http.MethodGet
	}
	return strings.ToUpper(c.Method)
}

// accepts reports whether status satisfies the check's success rule.
func (c Check) accepts(status int) bool {
	if c.ExpectStatus == 0 {
		return status < 500
	}
	return status == c.ExpectStatus
}

// Timing is the per-phase breakdown of one request, captured with
// net/http/httptrace.
type Timing struct {
	DNS      time.Duration `json:"dns"`
	Connect  time.Duration `json:"connect"`
	TLS      time.Duration `json:"tls"`
	TTFB     time.Duration `json:"ttfb"`     // from request written to first response byte
	Download time.Duration `json:"download"` // first byte to body fully read
	Total    time.Duration `json:"total"`
}

// Result is the outcome of one check run.
type Result struct {
	Check    Check
	When     time.Time
	OK       bool
	Status   int
	Err      string
	Timing   Timing
	Bytes    int64
	RemoteIP string // connected address (from httptrace GotConn)
	Proto    string // e.g. "HTTP/2.0"
	Edge     string // CDN edge/PoP identifier parsed from response headers, "" if unknown
	Server   string // Server header
}

// Run executes one check with a fresh connection (keep-alive is disabled so
// DNS, connect and TLS are measured on every run). It reads and discards up
// to maxBody bytes of the body. It never panics; any failure lands in
// Result.Err with OK=false.
func Run(ctx context.Context, c Check, timeout time.Duration) Result {
	res := Result{Check: c, When: time.Now()}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
	}
	if testTLSConfig != nil {
		tr.TLSClientConfig = testTLSConfig.Clone()
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}

	// Trace timestamps. Only the first occurrence of each phase is kept so a
	// redirect chain doesn't overwrite the values for the initial connection.
	var (
		mu                  sync.Mutex
		dnsStart, dnsDone   time.Time
		connStart, connDone time.Time
		tlsStart, tlsDone   time.Time
		wroteReq, firstByte time.Time
		remoteAddr          string
		setOnce             = func(t *time.Time) {
			mu.Lock()
			if t.IsZero() {
				*t = time.Now()
			}
			mu.Unlock()
		}
	)
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { setOnce(&dnsStart) },
		DNSDone:              func(httptrace.DNSDoneInfo) { setOnce(&dnsDone) },
		ConnectStart:         func(string, string) { setOnce(&connStart) },
		ConnectDone:          func(string, string, error) { setOnce(&connDone) },
		TLSHandshakeStart:    func() { setOnce(&tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { setOnce(&tlsDone) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { setOnce(&wroteReq) },
		GotFirstResponseByte: func() { setOnce(&firstByte) },
		GotConn: func(info httptrace.GotConnInfo) {
			mu.Lock()
			if remoteAddr == "" && info.Conn != nil && info.Conn.RemoteAddr() != nil {
				remoteAddr = info.Conn.RemoteAddr().String()
			}
			mu.Unlock()
		},
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), c.method(), c.URL, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Timing.Total = time.Since(start)
		res.Err = errString(err)
		fillPhases(&res.Timing, start, dnsStart, dnsDone, connStart, connDone, tlsStart, tlsDone, wroteReq, firstByte, time.Time{})
		mu.Lock()
		res.RemoteIP = hostOnly(remoteAddr)
		mu.Unlock()
		return res
	}
	defer resp.Body.Close()

	n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	end := time.Now()

	mu.Lock()
	res.RemoteIP = hostOnly(remoteAddr)
	fillPhases(&res.Timing, start, dnsStart, dnsDone, connStart, connDone, tlsStart, tlsDone, wroteReq, firstByte, end)
	mu.Unlock()
	res.Timing.Total = end.Sub(start)

	res.Status = resp.StatusCode
	res.Bytes = n
	res.Proto = resp.Proto
	res.Server = resp.Header.Get("Server")
	if prov, pop := EdgeFromHeaders(resp.Header); prov != "" {
		res.Edge = prov
		if pop != "" {
			res.Edge = prov + " " + pop
		}
	}

	switch {
	case readErr != nil && !errors.Is(readErr, io.EOF):
		res.Err = errString(readErr)
	case !c.accepts(resp.StatusCode):
		res.Err = fmt.Sprintf("unexpected status %d", resp.StatusCode)
	default:
		res.OK = true
	}
	return res
}

// fillPhases computes the phase durations from the trace timestamps. Caller
// must hold the trace mutex (or be past the request, when no callbacks fire).
func fillPhases(t *Timing, start, dnsStart, dnsDone, connStart, connDone, tlsStart, tlsDone, wroteReq, firstByte, end time.Time) {
	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		t.DNS = dnsDone.Sub(dnsStart)
	}
	if !connStart.IsZero() && !connDone.IsZero() {
		t.Connect = connDone.Sub(connStart)
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		t.TLS = tlsDone.Sub(tlsStart)
	}
	if !firstByte.IsZero() {
		from := wroteReq
		if from.IsZero() {
			from = start
		}
		t.TTFB = firstByte.Sub(from)
		if !end.IsZero() {
			t.Download = end.Sub(firstByte)
		}
	}
	for _, d := range []*time.Duration{&t.DNS, &t.Connect, &t.TLS, &t.TTFB, &t.Download} {
		if *d < 0 {
			*d = 0
		}
	}
}

// errString shortens net/http errors to their useful core.
func errString(err error) string {
	if err == nil {
		return ""
	}
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			if errors.Is(inner, context.DeadlineExceeded) {
				return "timeout"
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return err.Error()
}

func hostOnly(addr string) string {
	if addr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// EdgeFromHeaders identifies the CDN provider and, where exposed, the edge
// point-of-presence that served a response, from well-known headers.
// It returns ("", "") when nothing is recognised.
func EdgeFromHeaders(h http.Header) (provider, pop string) {
	server := strings.ToLower(h.Get("Server"))

	// Cloudflare: cf-ray "8a1b2c3d4e5f6789-EWR".
	if ray := h.Get("Cf-Ray"); ray != "" {
		if i := strings.LastIndex(ray, "-"); i >= 0 && i+1 < len(ray) {
			return "Cloudflare", ray[i+1:]
		}
		return "Cloudflare", ""
	}
	if strings.Contains(server, "cloudflare") {
		return "Cloudflare", ""
	}

	// CloudFront: x-amz-cf-pop "EWR52-C1".
	if p := h.Get("X-Amz-Cf-Pop"); p != "" {
		return "CloudFront", p
	}
	if strings.Contains(h.Get("Via"), "CloudFront") || strings.Contains(h.Get("X-Cache"), "cloudfront") {
		return "CloudFront", ""
	}

	// Fastly: x-served-by "cache-ewr18123-EWR" (may list several, comma-separated).
	if sb := h.Get("X-Served-By"); sb != "" && strings.Contains(strings.ToLower(sb), "cache-") {
		last := sb
		if i := strings.LastIndex(sb, ","); i >= 0 {
			last = strings.TrimSpace(sb[i+1:])
		}
		if i := strings.LastIndex(last, "-"); i >= 0 && i+1 < len(last) {
			return "Fastly", last[i+1:]
		}
		return "Fastly", ""
	}
	if strings.Contains(h.Get("Via"), "varnish") && h.Get("X-Timer") != "" {
		return "Fastly", ""
	}

	// Akamai: server "AkamaiGHost"/"AkamaiNetStorage", or any x-akamai-* header.
	if strings.Contains(server, "akamai") {
		return "Akamai", akamaiPop(h)
	}
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "x-akamai-") {
			return "Akamai", akamaiPop(h)
		}
	}
	if xc := h.Get("X-Cache"); xc != "" && strings.Contains(strings.ToLower(xc), "akamai") {
		return "Akamai", xc
	}

	// Google front ends.
	if server == "gws" || server == "sffe" || server == "esf" || strings.HasPrefix(server, "gse") ||
		strings.Contains(strings.ToLower(h.Get("Via")), "google") {
		return "Google", ""
	}
	return "", ""
}

// akamaiPop returns the most informative Akamai-specific value available, if any.
func akamaiPop(h http.Header) string {
	for _, k := range []string{"X-Akamai-Request-Id", "X-Cache", "X-Akamai-Staging"} {
		if v := h.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// Stats is the rolling summary of the last N results for one check.
type Stats struct {
	Runs, Fails  int
	Availability float64 // fraction OK in window, 0..1 (0 if Runs==0)
	Consecutive  int     // consecutive failures ending at the newest result

	Mean, P50, P95, Max time.Duration // of Timing.Total, successful runs only

	MeanDNS, MeanConnect, MeanTLS, MeanTTFB time.Duration

	Last Result // newest result (zero When if none)
}

// Down reports whether the check has failed twice or more in a row.
func (s Stats) Down() bool { return s.Consecutive >= 2 }

// Slow reports whether responses are sluggish even when they succeed.
func (s Stats) Slow() bool {
	return s.MeanTTFB > 800*time.Millisecond || s.Mean > 2*time.Second
}

// Summary pairs a check with its current rolling statistics.
type Summary struct {
	Check Check
	Stats Stats
}

// Monitor holds a rolling window of results per check, keyed by URL. It is
// safe for concurrent use.
type Monitor struct {
	mu      sync.RWMutex
	checks  []Check
	window  int
	results map[string][]Result
}

// NewMonitor creates a Monitor for the given checks keeping the last window
// results of each (default 20).
func NewMonitor(checks []Check, window int) *Monitor {
	if window <= 0 {
		window = 20
	}
	m := &Monitor{window: window, results: make(map[string][]Result)}
	m.SetChecks(checks)
	return m
}

// Checks returns a copy of the configured checks in their original order.
func (m *Monitor) Checks() []Check {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Check(nil), m.checks...)
}

// SetChecks replaces the check set. Result windows for URLs that remain are
// kept; windows for removed URLs are dropped.
func (m *Monitor) SetChecks(checks []Check) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checks = append([]Check(nil), checks...)
	keep := make(map[string]bool, len(checks))
	for _, c := range checks {
		keep[c.URL] = true
	}
	for url := range m.results {
		if !keep[url] {
			delete(m.results, url)
		}
	}
}

// Add records a result into its check's window.
func (m *Monitor) Add(r Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := append(m.results[r.Check.URL], r)
	if len(w) > m.window {
		w = w[len(w)-m.window:]
	}
	m.results[r.Check.URL] = w
}

// Summaries returns the rolling stats for every check, in Checks() order. A
// check with no results yet has zero Stats.
func (m *Monitor) Summaries() []Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Summary, 0, len(m.checks))
	for _, c := range m.checks {
		out = append(out, Summary{Check: c, Stats: computeStats(m.results[c.URL])})
	}
	return out
}

// RunAll runs every enabled check with bounded concurrency and records the
// results. It returns when all checks have finished or ctx is cancelled.
func (m *Monitor) RunAll(ctx context.Context, timeout time.Duration, parallel int) {
	if parallel <= 0 {
		parallel = 4
	}
	checks := m.Checks()
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, c := range checks {
		if !c.IsEnabled() {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c Check) {
			defer wg.Done()
			defer func() { <-sem }()
			m.Add(Run(ctx, c, timeout))
		}(c)
	}
	wg.Wait()
}

// computeStats summarises a window of results (oldest first).
func computeStats(w []Result) Stats {
	var s Stats
	s.Runs = len(w)
	if s.Runs == 0 {
		return s
	}
	s.Last = w[len(w)-1]

	totals := make([]float64, 0, len(w))
	var sumTotal, sumDNS, sumConn, sumTLS, sumTTFB time.Duration
	for _, r := range w {
		if !r.OK {
			s.Fails++
			continue
		}
		totals = append(totals, float64(r.Timing.Total))
		sumTotal += r.Timing.Total
		sumDNS += r.Timing.DNS
		sumConn += r.Timing.Connect
		sumTLS += r.Timing.TLS
		sumTTFB += r.Timing.TTFB
	}
	for i := len(w) - 1; i >= 0 && !w[i].OK; i-- {
		s.Consecutive++
	}
	s.Availability = float64(s.Runs-s.Fails) / float64(s.Runs)

	if ok := len(totals); ok > 0 {
		n := time.Duration(ok)
		s.Mean = sumTotal / n
		s.MeanDNS = sumDNS / n
		s.MeanConnect = sumConn / n
		s.MeanTLS = sumTLS / n
		s.MeanTTFB = sumTTFB / n
		sort.Float64s(totals)
		s.P50 = time.Duration(percentile(totals, 50))
		s.P95 = time.Duration(percentile(totals, 95))
		s.Max = time.Duration(totals[ok-1])
	}
	return s
}

// percentile returns the p-th percentile of an ascending slice using the
// nearest-rank method. sorted must be non-empty.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 1 || p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[n-1]
	}
	rank := int(float64(n)*p/100+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= n {
		rank = n - 1
	}
	return sorted[rank]
}

// RateTTFB buckets time-to-first-byte for UI colouring.
func RateTTFB(d time.Duration) metrics.Rating {
	ms := d.Milliseconds()
	switch {
	case d <= 0:
		return metrics.RatingUnknown
	case ms < 200:
		return metrics.RatingExcellent
	case ms < 500:
		return metrics.RatingGood
	case ms < 1000:
		return metrics.RatingPlayable
	default:
		return metrics.RatingPoor
	}
}

// RateTotal buckets total request time for UI colouring.
func RateTotal(d time.Duration) metrics.Rating {
	ms := d.Milliseconds()
	switch {
	case d <= 0:
		return metrics.RatingUnknown
	case ms < 500:
		return metrics.RatingExcellent
	case ms < 1000:
		return metrics.RatingGood
	case ms < 2500:
		return metrics.RatingPlayable
	default:
		return metrics.RatingPoor
	}
}
