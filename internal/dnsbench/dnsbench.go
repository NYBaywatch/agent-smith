// Package dnsbench races the configured DNS resolver against well-known public
// ones on real-world names — plus one deliberately uncached lookup — and
// recommends the fastest. Slow name resolution makes every new connection
// feel sluggish even when latency and loss are perfect, and the fix (changing
// one setting) is the cheapest win in networking.
package dnsbench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
)

// Resolver identifies a nameserver to benchmark. An empty Addr means the
// system-configured resolver; otherwise Addr is "ip:53".
type Resolver struct {
	Name string
	Addr string
}

// SystemName is the display name of the configured resolver.
const SystemName = "System"

// DefaultResolvers returns the standard field: the system resolver, the major
// public resolvers, and the LAN gateway (most routers run a forwarder) when
// its IP is known.
func DefaultResolvers(gatewayIP string) []Resolver {
	rs := []Resolver{
		{Name: SystemName, Addr: ""},
		{Name: "Cloudflare", Addr: "1.1.1.1:53"},
		{Name: "Google", Addr: "8.8.8.8:53"},
		{Name: "Quad9", Addr: "9.9.9.9:53"},
		{Name: "OpenDNS", Addr: "208.67.222.222:53"},
	}
	if gatewayIP != "" {
		rs = append(rs, Resolver{Name: "Gateway", Addr: net.JoinHostPort(gatewayIP, "53")})
	}
	return rs
}

// DefaultDomains are popular names every resolver has hot in its cache, so the
// timing reflects the resolver's own speed rather than upstream authority.
var DefaultDomains = []string{
	"google.com", "youtube.com", "microsoft.com", "apple.com", "amazon.com", "cloudflare.com",
	"github.com", "netflix.com", "zoom.us", "steampowered.com", "openai.com", "wikipedia.org",
}

// Options configures a benchmark run.
type Options struct {
	Resolvers []Resolver
	Domains   []string
	Rounds    int           // passes over Domains per resolver (default 2)
	Timeout   time.Duration // per-query bound (default 2 s)
	Progress  func(done, total int)
}

// ResolverResult is one resolver's scorecard.
type ResolverResult struct {
	Name    string
	Addr    string
	Queries int
	Failed  int
	Median  time.Duration
	P95     time.Duration
	Max     time.Duration
	// Uncached is the time to answer a name no cache can hold (a random label
	// under an example.com zone); NXDOMAIN counts as answered. 0 if it failed.
	Uncached time.Duration
	OK       bool // answered at least one query
	Rank     int  // 1 = fastest
	Rating   metrics.Rating
}

// Result is the whole benchmark.
type Result struct {
	When           time.Time
	Duration       time.Duration
	Resolvers      []ResolverResult // fastest first; failed resolvers last
	Fastest        string           // name of the fastest working resolver
	Current        string           // name of the configured resolver entry (SystemName) if present
	Recommendation string
}

// lookupHost performs one timed query; tests replace it.
var lookupHost = func(ctx context.Context, r *net.Resolver, host string) ([]string, error) {
	return r.LookupHost(ctx, host)
}

// resolverAddrs remembers which address each resolver was built for, so
// tests that replace lookupHost can tell resolvers apart.
var resolverAddrs sync.Map // *net.Resolver → addr

// resolverFor returns a resolver that sends queries straight to addr over
// UDP, or the system default when addr is empty.
func resolverFor(addr string, timeout time.Duration) *net.Resolver {
	var r *net.Resolver
	if addr == "" {
		r = &net.Resolver{}
	} else {
		target := addr
		r = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: timeout}
				return d.DialContext(ctx, "udp", target)
			},
		}
	}
	resolverAddrs.Store(r, addr)
	return r
}

// Run benchmarks every resolver in parallel (queries within a resolver are
// sequential so they measure latency, not concurrency).
func Run(ctx context.Context, opt Options) (Result, error) {
	if len(opt.Resolvers) == 0 {
		opt.Resolvers = DefaultResolvers("")
	}
	if len(opt.Domains) == 0 {
		opt.Domains = DefaultDomains
	}
	if opt.Rounds <= 0 {
		opt.Rounds = 2
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Second
	}
	res := Result{When: time.Now()}
	perResolver := opt.Rounds*len(opt.Domains) + 1
	total := perResolver * len(opt.Resolvers)

	var mu sync.Mutex
	done := 0
	tick := func() {
		mu.Lock()
		done++
		d := done
		mu.Unlock()
		if opt.Progress != nil {
			opt.Progress(d, total)
		}
	}

	results := make([]ResolverResult, len(opt.Resolvers))
	var wg sync.WaitGroup
	for i, rv := range opt.Resolvers {
		wg.Add(1)
		go func(i int, rv Resolver) {
			defer wg.Done()
			results[i] = benchOne(ctx, rv, opt, tick)
		}(i, rv)
	}
	wg.Wait()
	res.Duration = time.Since(res.When)

	sort.SliceStable(results, func(a, b int) bool {
		ra, rb := results[a], results[b]
		if ra.OK != rb.OK {
			return ra.OK
		}
		return ra.Median < rb.Median
	})
	for i := range results {
		results[i].Rank = i + 1
		if results[i].Name == SystemName {
			res.Current = SystemName
		}
	}
	if len(results) > 0 && results[0].OK {
		res.Fastest = results[0].Name
	}
	res.Resolvers = results
	res.Recommendation = Recommend(res)
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, nil
}

func benchOne(ctx context.Context, rv Resolver, opt Options, tick func()) ResolverResult {
	rr := ResolverResult{Name: rv.Name, Addr: rv.Addr}
	r := resolverFor(rv.Addr, opt.Timeout)
	var times []float64
	query := func(host string) (time.Duration, bool) {
		qctx, cancel := context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
		start := time.Now()
		_, err := lookupHost(qctx, r, host)
		el := time.Since(start)
		if err != nil {
			var de *net.DNSError
			if errors.As(err, &de) && de.IsNotFound {
				return el, true // answered promptly: the name simply doesn't exist
			}
			return 0, false
		}
		return el, true
	}
	for round := 0; round < opt.Rounds; round++ {
		for _, d := range opt.Domains {
			if ctx.Err() != nil {
				break
			}
			rr.Queries++
			if el, ok := query(d); ok {
				times = append(times, float64(el))
			} else {
				rr.Failed++
			}
			tick()
		}
	}
	if ctx.Err() == nil {
		if el, ok := query(uncachedName()); ok {
			rr.Uncached = el
		}
	}
	tick()

	rr.OK = len(times) > 0
	if rr.OK {
		sort.Float64s(times)
		rr.Median = time.Duration(times[len(times)/2])
		rr.P95 = time.Duration(times[pctIndex(len(times), 95)])
		rr.Max = time.Duration(times[len(times)-1])
		rr.Rating = rate(rr.Median)
	} else {
		rr.Rating = metrics.RatingPoor
	}
	return rr
}

func pctIndex(n int, p int) int {
	i := (n*p + 99) / 100 // nearest rank, 1-based
	if i < 1 {
		i = 1
	}
	if i > n {
		i = n
	}
	return i - 1
}

func uncachedName() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + ".agent-smith-bench.example.com"
}

func rate(median time.Duration) metrics.Rating {
	switch {
	case median < 20*time.Millisecond:
		return metrics.RatingExcellent
	case median < 50*time.Millisecond:
		return metrics.RatingGood
	case median < 100*time.Millisecond:
		return metrics.RatingPlayable
	default:
		return metrics.RatingPoor
	}
}

// Recommend turns the ranking into one plain-language action.
func Recommend(r Result) string {
	if len(r.Resolvers) == 0 || r.Fastest == "" {
		return "No resolver answered — check that DNS traffic is allowed on this network."
	}
	fastest := r.Resolvers[0]
	var system *ResolverResult
	for i := range r.Resolvers {
		if r.Resolvers[i].Name == SystemName {
			system = &r.Resolvers[i]
			break
		}
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	where := fastest.Name
	if fastest.Addr != "" {
		where += " (" + fastest.Addr + ")"
	}
	if system == nil {
		return fmt.Sprintf("Fastest resolver: %s, median %.0f ms.", where, ms(fastest.Median))
	}
	if !system.OK {
		return fmt.Sprintf("Your configured resolver is failing lookups; switch to %s (median %.0f ms).", where, ms(fastest.Median))
	}
	// A sub-2 ms system median is the OS cache answering, not the resolver;
	// judge it by its cache-miss time against the fastest public resolver.
	if system.Median < 2*time.Millisecond && system.Uncached > 0 {
		var pub *ResolverResult
		for i := range r.Resolvers {
			// The gateway is a caching forwarder to the same upstream, so it is
			// not an alternative; compare against the fastest public resolver.
			if r.Resolvers[i].Name != SystemName && r.Resolvers[i].Name != "Gateway" && r.Resolvers[i].OK {
				pub = &r.Resolvers[i]
				break
			}
		}
		if pub != nil {
			pw := pub.Name
			if pub.Addr != "" {
				pw += " (" + pub.Addr + ")"
			}
			if system.Uncached-pub.Median <= 10*time.Millisecond {
				return fmt.Sprintf("Your configured resolver answers cached names from the Windows cache (%.1f ms) and a cache miss took %.0f ms — on par with the fastest public resolver, %s at %.0f ms. Nothing to change.", ms(system.Median), ms(system.Uncached), pw, ms(pub.Median))
			}
			return fmt.Sprintf("Your configured resolver answers cached names instantly, but a cache miss took %.0f ms while %s answers in %.0f ms median — switching would make new sites feel snappier.", ms(system.Uncached), pw, ms(pub.Median))
		}
	}
	if system.Name == fastest.Name || system.Median-fastest.Median <= 10*time.Millisecond {
		return fmt.Sprintf("Your configured resolver is already the fastest (median %.0f ms) — nothing to change.", ms(system.Median))
	}
	diff := ms(system.Median - fastest.Median)
	return fmt.Sprintf("Switch your DNS to %s: %.0f ms vs %.0f ms — roughly %.0f ms faster per lookup.", where, ms(fastest.Median), ms(system.Median), diff)
}
