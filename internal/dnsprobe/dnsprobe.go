// Package dnsprobe measures DNS resolution latency. Slow name resolution feels
// like "the internet is laggy" even when RTT/loss are fine, so Agent Smith
// tracks it as a distinct signal (and can point users at a faster resolver).
package dnsprobe

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Result summarizes a round of DNS lookups.
type Result struct {
	Avg     time.Duration // average successful lookup latency
	Max     time.Duration
	Lookups int // attempted
	Failed  int // failures (NXDOMAIN excluded — see Measure)
}

// defaultDomains are stable, widely-resolvable names used to gauge resolver speed.
var defaultDomains = []string{"google.com", "cloudflare.com", "github.com"}

// Server identifies a specific resolver to probe directly. An empty Addr means
// the system default resolver; otherwise Addr is a "host:port" (e.g. "1.1.1.1:53").
type Server struct {
	Name string
	Addr string
}

// ServerResult is a per-resolver measurement, for comparing resolvers.
type ServerResult struct {
	Name    string
	Addr    string
	Avg     time.Duration
	Lookups int
	Failed  int
}

// OK reports whether the resolver answered at least one lookup.
func (s ServerResult) OK() bool { return s.Lookups > 0 && s.Failed < s.Lookups }

// Slow reports whether the resolver's average latency is sluggish.
func (s ServerResult) Slow() bool { return s.Avg > 100*time.Millisecond }

// resolverFor returns a resolver that sends queries directly to addr (over UDP),
// or the system default resolver when addr is empty.
func resolverFor(addr string, perTimeout time.Duration) *net.Resolver {
	if addr == "" {
		return &net.Resolver{}
	}
	target := addr
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: perTimeout}
			return d.DialContext(ctx, "udp", target)
		},
	}
}

// MeasureServers probes each resolver directly and returns a per-resolver
// comparison, so a slow *configured* resolver stands out against alternatives.
func MeasureServers(ctx context.Context, servers []Server, domains []string, perTimeout time.Duration) []ServerResult {
	if len(domains) == 0 {
		domains = defaultDomains
	}
	if perTimeout <= 0 {
		perTimeout = 2 * time.Second
	}
	out := make([]ServerResult, 0, len(servers))
	for _, s := range servers {
		r := measureWith(ctx, resolverFor(s.Addr, perTimeout), domains, perTimeout)
		out = append(out, ServerResult{Name: s.Name, Addr: s.Addr, Avg: r.Avg, Lookups: r.Lookups, Failed: r.Failed})
	}
	return out
}

// Measure resolves a set of domains using the system resolver and reports the
// average/max latency. A nil/empty domains slice uses a sensible default set.
// perTimeout bounds each individual lookup.
func Measure(ctx context.Context, domains []string, perTimeout time.Duration) Result {
	if len(domains) == 0 {
		domains = defaultDomains
	}
	if perTimeout <= 0 {
		perTimeout = 2 * time.Second
	}
	return measureWith(ctx, &net.Resolver{}, domains, perTimeout)
}

// measureWith runs the lookups against a specific resolver.
func measureWith(ctx context.Context, r *net.Resolver, domains []string, perTimeout time.Duration) Result {
	var res Result
	var sum time.Duration
	var ok int

	for _, d := range domains {
		res.Lookups++
		lctx, cancel := context.WithTimeout(ctx, perTimeout)
		start := time.Now()
		_, err := r.LookupHost(lctx, d)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			// NXDOMAIN means the resolver answered promptly (name simply does
			// not exist) — that is a successful latency measurement, not a
			// resolver failure, so we record it rather than counting it failed.
			var de *net.DNSError
			if errors.As(err, &de) && de.IsNotFound {
				ok++
				sum += elapsed
				if elapsed > res.Max {
					res.Max = elapsed
				}
				continue
			}
			res.Failed++
			continue
		}
		ok++
		sum += elapsed
		if elapsed > res.Max {
			res.Max = elapsed
		}
	}
	if ok > 0 {
		res.Avg = sum / time.Duration(ok)
	}
	return res
}

// Rate buckets average DNS latency for display. These thresholds reflect typical
// resolver expectations (a local/cached or fast public resolver answers in tens
// of ms; hundreds of ms is sluggish).
func (r Result) Slow() bool { return r.Avg > 100*time.Millisecond }

// --- authoritative nameserver timing ---

// AuthResult is one timed query sent directly to a domain's authoritative
// nameserver — the "nameserver resolution time" that a recursive resolver's
// cache normally hides. A slow or failing authoritative server makes a site
// slow to reach on every cache miss, independently of the user's resolver.
type AuthResult struct {
	Domain  string
	NS      string // nameserver hostname (e.g. "ns1.example.com.")
	Addr    string // "ip:53" actually queried; empty if the NS could not be resolved
	Latency time.Duration
	OK      bool
	Err     string
}

// maxAuthNS caps how many nameservers are timed per domain.
const maxAuthNS = 2

// authConcurrency bounds how many domains are measured at once.
const authConcurrency = 3

// Lookups used by MeasureAuthoritative, as package variables so tests can
// script them without the network.
var (
	lookupNS = func(ctx context.Context, domain string) ([]*net.NS, error) {
		return net.DefaultResolver.LookupNS(ctx, domain)
	}
	lookupHost = func(ctx context.Context, host string) ([]string, error) {
		return net.DefaultResolver.LookupHost(ctx, host)
	}
	// resolverForHook builds the resolver used for the timed query; tests
	// redirect it at an in-process responder.
	resolverForHook = resolverFor
)

// MeasureAuthoritative times, for each domain, one lookup of the domain sent
// straight to up to two of its authoritative nameservers (found via NS
// records and resolved to addresses first). Each query is bounded by ctx and
// perTimeout; domains are measured concurrently (bounded) to cap wall time.
// A domain whose NS records cannot be found yields a single failed result.
func MeasureAuthoritative(ctx context.Context, domains []string, perTimeout time.Duration) []AuthResult {
	if perTimeout <= 0 {
		perTimeout = 2 * time.Second
	}
	per := make([][]AuthResult, len(domains))
	sem := make(chan struct{}, authConcurrency)
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				per[i] = []AuthResult{{Domain: d, Err: ctx.Err().Error()}}
				return
			}
			per[i] = measureAuthDomain(ctx, d, perTimeout)
		}(i, d)
	}
	wg.Wait()
	var out []AuthResult
	for _, rs := range per {
		out = append(out, rs...)
	}
	return out
}

// measureAuthDomain times queries for one domain against its nameservers.
func measureAuthDomain(ctx context.Context, domain string, perTimeout time.Duration) []AuthResult {
	nctx, cancel := context.WithTimeout(ctx, perTimeout)
	nss, err := lookupNS(nctx, domain)
	cancel()
	if err != nil {
		return []AuthResult{{Domain: domain, Err: err.Error()}}
	}
	if len(nss) == 0 {
		return []AuthResult{{Domain: domain, Err: "no NS records"}}
	}
	if len(nss) > maxAuthNS {
		nss = nss[:maxAuthNS]
	}
	out := make([]AuthResult, 0, len(nss))
	for _, ns := range nss {
		r := AuthResult{Domain: domain, NS: ns.Host}
		hctx, cancel := context.WithTimeout(ctx, perTimeout)
		addrs, err := lookupHost(hctx, strings.TrimSuffix(ns.Host, "."))
		cancel()
		if err != nil || len(addrs) == 0 {
			r.Err = "nameserver address unknown"
			if err != nil {
				r.Err = err.Error()
			}
			out = append(out, r)
			continue
		}
		r.Addr = net.JoinHostPort(addrs[0], "53")
		qctx, cancel := context.WithTimeout(ctx, perTimeout)
		start := time.Now()
		_, err = resolverForHook(r.Addr, perTimeout).LookupHost(qctx, domain)
		r.Latency = time.Since(start)
		cancel()
		if err != nil {
			var de *net.DNSError
			if !(errors.As(err, &de) && de.IsNotFound) {
				r.Err = err.Error()
				out = append(out, r)
				continue
			}
		}
		r.OK = true
		out = append(out, r)
	}
	return out
}

// SystemResolvers returns the resolver addresses the OS is configured with,
// best effort: /etc/resolv.conf nameserver entries on Unix-like systems, nil on
// Windows (or when nothing can be read) so callers fall back to the system
// resolver.
func SystemResolvers() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
			out = append(out, f[1])
		}
	}
	return out
}
