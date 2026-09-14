package dnsbench

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
)

// scriptLookups routes each timed query through a table keyed by resolver
// address ("" = system). A negative latency means "fail"; the uncached name
// gets an NXDOMAIN, which must count as answered.
func scriptLookups(t *testing.T, latency map[string]time.Duration) {
	t.Helper()
	orig := lookupHost
	lookupHost = func(ctx context.Context, r *net.Resolver, host string) ([]string, error) {
		addr := ""
		if v, ok := resolverAddrs.Load(r); ok {
			addr = v.(string)
		}
		d, known := latency[addr]
		if !known || d < 0 {
			return nil, errors.New("server failure")
		}
		time.Sleep(d)
		if strings.Contains(host, "agent-smith-bench") {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return []string{"192.0.2.1"}, nil
	}
	t.Cleanup(func() { lookupHost = orig })
}

func TestRankingAndRecommendation(t *testing.T) {
	scriptLookups(t, map[string]time.Duration{
		"":             30 * time.Millisecond, // system
		"127.0.0.1:53": 5 * time.Millisecond,  // "Fast"
		"127.0.0.2:53": -1,                    // "Broken"
		"127.0.0.3:53": 60 * time.Millisecond, // "Slow"
	})
	var progress []int
	var mu sync.Mutex
	opt := Options{
		Resolvers: []Resolver{{Name: SystemName}, {Name: "Fast", Addr: "127.0.0.1:53"}, {Name: "Broken", Addr: "127.0.0.2:53"}, {Name: "Slow", Addr: "127.0.0.3:53"}},
		Domains:   []string{"a.test", "b.test"},
		Rounds:    2,
		Timeout:   time.Second,
		Progress:  func(done, total int) { mu.Lock(); progress = append(progress, total); mu.Unlock() },
	}
	r, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, rr := range r.Resolvers {
		names = append(names, rr.Name)
	}
	if strings.Join(names, ",") != "Fast,System,Slow,Broken" {
		t.Fatalf("order %v", names)
	}
	if r.Resolvers[0].Rank != 1 || r.Resolvers[3].OK || r.Resolvers[3].Rank != 4 {
		t.Errorf("ranks/OK wrong: %+v", r.Resolvers)
	}
	if r.Resolvers[0].Queries != 4 || r.Resolvers[0].Failed != 0 || r.Resolvers[0].Uncached <= 0 {
		t.Errorf("fast: %+v (uncached NXDOMAIN must count as answered)", r.Resolvers[0])
	}
	if r.Resolvers[3].Failed != 4 || r.Resolvers[3].Rating != metrics.RatingPoor {
		t.Errorf("broken: %+v", r.Resolvers[3])
	}
	if r.Fastest != "Fast" || r.Current != SystemName {
		t.Errorf("fastest %q current %q", r.Fastest, r.Current)
	}
	if !strings.HasPrefix(r.Recommendation, "Switch your DNS to Fast (127.0.0.1:53)") {
		t.Errorf("recommendation %q", r.Recommendation)
	}
	// Progress: total == (rounds*domains + 1) * resolvers, reported that many times.
	want := (2*2 + 1) * 4
	if len(progress) != want || progress[0] != want {
		t.Errorf("progress calls %d total %v want %d", len(progress), progress[:1], want)
	}
	if r.Resolvers[0].Rating != metrics.RatingExcellent || r.Resolvers[2].Rating != metrics.RatingPlayable {
		t.Errorf("ratings: %v %v", r.Resolvers[0].Rating, r.Resolvers[2].Rating)
	}
}

func TestRecommendVariants(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	cases := []struct {
		name string
		r    Result
		want string
	}{
		{"none", Result{}, "No resolver answered"},
		{"system fastest", Result{Fastest: SystemName, Resolvers: []ResolverResult{{Name: SystemName, OK: true, Median: ms(8)}, {Name: "Google", Addr: "8.8.8.8:53", OK: true, Median: ms(20)}}}, "already the fastest"},
		{"within 10ms", Result{Fastest: "Google", Resolvers: []ResolverResult{{Name: "Google", Addr: "8.8.8.8:53", OK: true, Median: ms(12)}, {Name: SystemName, OK: true, Median: ms(20)}}}, "already the fastest"},
		{"switch", Result{Fastest: "Google", Resolvers: []ResolverResult{{Name: "Google", Addr: "8.8.8.8:53", OK: true, Median: ms(12)}, {Name: SystemName, OK: true, Median: ms(80)}}}, "Switch your DNS to Google (8.8.8.8:53): 12 ms vs 80 ms"},
		{"system failing", Result{Fastest: "Google", Resolvers: []ResolverResult{{Name: "Google", Addr: "8.8.8.8:53", OK: true, Median: ms(12)}, {Name: SystemName, OK: false}}}, "failing lookups; switch to Google"},
		{"no system entry", Result{Fastest: "Google", Resolvers: []ResolverResult{{Name: "Google", Addr: "8.8.8.8:53", OK: true, Median: ms(12)}}}, "Fastest resolver: Google"},
	}
	for _, c := range cases {
		if got := Recommend(c.r); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not contain %q", c.name, got, c.want)
		}
	}
}

func TestDefaultResolvers(t *testing.T) {
	if rs := DefaultResolvers(""); len(rs) != 5 || rs[0].Name != SystemName || rs[0].Addr != "" {
		t.Errorf("without gateway: %+v", rs)
	}
	rs := DefaultResolvers("192.168.1.1")
	if len(rs) != 6 || rs[5].Name != "Gateway" || rs[5].Addr != "192.168.1.1:53" {
		t.Errorf("with gateway: %+v", rs)
	}
}

func TestPctIndex(t *testing.T) {
	if pctIndex(1, 95) != 0 || pctIndex(20, 95) != 18 || pctIndex(100, 95) != 94 {
		t.Errorf("pctIndex: %d %d %d", pctIndex(1, 95), pctIndex(20, 95), pctIndex(100, 95))
	}
}
