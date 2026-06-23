// Package dnsprobe measures DNS resolution latency. Slow name resolution feels
// like "the internet is laggy" even when RTT/loss are fine, so Agent Smith
// tracks it as a distinct signal (and can point users at a faster resolver).
package dnsprobe

import (
	"context"
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
	var res Result
	var sum time.Duration
	var ok int
	r := &net.Resolver{}

	for _, d := range domains {
		res.Lookups++
		lctx, cancel := context.WithTimeout(ctx, perTimeout)
		start := time.Now()
		_, err := r.LookupHost(lctx, d)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
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
