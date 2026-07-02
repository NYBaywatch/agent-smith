// Package dnsprobe measures DNS resolution latency. Slow name resolution feels
// like "the internet is laggy" even when RTT/loss are fine, so Agent Smith
// tracks it as a distinct signal (and can point users at a faster resolver).
package dnsprobe

import (
	"context"
	"errors"
	"net"
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
