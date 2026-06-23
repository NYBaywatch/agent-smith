// Package bufferbloat measures latency-under-load: the increase in RTT while the
// link is saturated. This is the single most under-reported metric for gamers
// ("my ping is fine until someone streams Netflix"). It establishes an idle
// baseline, saturates the downlink with parallel HTTP transfers, samples RTT
// during the load, and grades the delta on the DSLReports A+…F scale.
package bufferbloat

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// userAgent is sent with load requests; some CDNs (e.g. Cloudflare) reject the
// default Go user agent with HTTP 403.
const userAgent = "AgentSmith/1.0 (+https://github.com/NYBaywatch/agent-smith)"

// Options configures a bufferbloat run.
type Options struct {
	// LoadURLs are candidate large-file endpoints; the first that responds 200
	// is used to saturate the link. Defaults to a list of public test files.
	LoadURLs []string
	// Connections is the number of parallel download streams used to saturate.
	Connections int
	// WarmUp is ignored for the baseline; it lets the download ramp before
	// loaded-latency sampling begins.
	WarmUp time.Duration
	// LoadDuration is how long to sample latency under load.
	LoadDuration time.Duration
	// PingInterval is the cadence of probes during both phases.
	PingInterval time.Duration
	// PingTarget is the host pinged to observe latency. Defaults to 1.1.1.1.
	PingTarget net.IP
	// BaselineSamples is the number of idle probes for the baseline.
	BaselineSamples int
}

// DefaultOptions returns sensible defaults (~10s total test).
func DefaultOptions() Options {
	return Options{
		LoadURLs: []string{
			"https://proof.ovh.net/files/100Mb.dat",
			"http://ipv4.download.thinkbroadband.com/100MB.zip",
		},
		Connections:     4,
		WarmUp:          1500 * time.Millisecond,
		LoadDuration:    7 * time.Second,
		PingInterval:    200 * time.Millisecond,
		PingTarget:      net.IPv4(1, 1, 1, 1),
		BaselineSamples: 10,
	}
}

// Result holds the outcome of a bufferbloat measurement.
type Result struct {
	IdleRTT      time.Duration // baseline median RTT
	LoadedRTT    time.Duration // median RTT under load
	Added        time.Duration // LoadedRTT - IdleRTT (clamped at 0)
	Grade        string        // DSLReports A+…F
	DownloadMbps float64       // throughput achieved during the load phase
	IdleSamples  int
	LoadSamples  int
}

// Run executes a download-saturation bufferbloat test. It is safe to cancel via
// ctx. Upload-direction testing is planned; this measures the download path.
func Run(ctx context.Context, p probe.Pinger, opt Options) (Result, error) {
	o := DefaultOptions()
	if len(opt.LoadURLs) > 0 {
		o.LoadURLs = opt.LoadURLs
	}
	if opt.Connections > 0 {
		o.Connections = opt.Connections
	}
	if opt.WarmUp > 0 {
		o.WarmUp = opt.WarmUp
	}
	if opt.LoadDuration > 0 {
		o.LoadDuration = opt.LoadDuration
	}
	if opt.PingInterval > 0 {
		o.PingInterval = opt.PingInterval
	}
	if opt.PingTarget != nil {
		o.PingTarget = opt.PingTarget
	}
	if opt.BaselineSamples > 0 {
		o.BaselineSamples = opt.BaselineSamples
	}

	var res Result

	// Phase 1: idle baseline.
	idle := samplePings(ctx, p, o.PingTarget, o.BaselineSamples, o.PingInterval)
	res.IdleSamples = len(idle)
	if len(idle) == 0 {
		return res, fmt.Errorf("bufferbloat: no idle baseline samples (target unreachable?)")
	}
	res.IdleRTT = median(idle)

	// Pick a load endpoint that actually serves us (some CDNs 403 non-browser
	// clients); fail clearly rather than reporting a bogus grade with no load.
	loadURL := pickWorkingURL(ctx, o.LoadURLs)
	if loadURL == "" {
		return res, fmt.Errorf("bufferbloat: no reachable download endpoint (tried %d)", len(o.LoadURLs))
	}

	// Phase 2: saturate + sample under load.
	loadCtx, cancelLoad := context.WithCancel(ctx)
	var bytesRead atomic.Uint64
	var wg sync.WaitGroup
	for i := 0; i < o.Connections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			saturate(loadCtx, loadURL, &bytesRead)
		}()
	}

	// Warm up the transfer before measuring loaded latency.
	select {
	case <-time.After(o.WarmUp):
	case <-ctx.Done():
		cancelLoad()
		wg.Wait()
		return res, ctx.Err()
	}

	startBytes := bytesRead.Load()
	startTime := time.Now()
	loaded := samplePingsDuration(loadCtx, p, o.PingTarget, o.LoadDuration, o.PingInterval)
	elapsed := time.Since(startTime).Seconds()
	endBytes := bytesRead.Load()

	cancelLoad()
	wg.Wait()

	res.LoadSamples = len(loaded)
	if elapsed > 0 {
		res.DownloadMbps = float64(endBytes-startBytes) * 8 / 1e6 / elapsed
	}
	// If essentially nothing downloaded, the link was never saturated, so any
	// grade would be meaningless — report the failure instead.
	if endBytes-startBytes < 256*1024 {
		return res, fmt.Errorf("bufferbloat: link was not saturated (only %d bytes downloaded); result is not meaningful", endBytes-startBytes)
	}
	if len(loaded) == 0 {
		return res, fmt.Errorf("bufferbloat: no loaded samples")
	}
	res.LoadedRTT = median(loaded)

	added := res.LoadedRTT - res.IdleRTT
	if added < 0 {
		added = 0
	}
	res.Added = added
	res.Grade = metrics.BufferbloatGrade(added)
	return res, nil
}

// saturate downloads from url until ctx is cancelled, counting bytes read.
func saturate(ctx context.Context, url string, counter *atomic.Uint64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			counter.Add(uint64(n))
		}
		if err != nil {
			return
		}
	}
}

// pickWorkingURL returns the first URL that responds 200 to a GET with our
// user agent (reading a little to confirm a real body), or "" if none do.
func pickWorkingURL(ctx context.Context, urls []string) string {
	for _, url := range urls {
		if ctx.Err() != nil {
			return ""
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			continue
		}
		ok := resp.StatusCode == http.StatusOK
		if ok {
			buf := make([]byte, 32*1024)
			n, _ := resp.Body.Read(buf)
			ok = n > 0
		}
		resp.Body.Close()
		cancel()
		if ok {
			return url
		}
	}
	return ""
}

func samplePings(ctx context.Context, p probe.Pinger, target net.IP, count int, interval time.Duration) []time.Duration {
	var out []time.Duration
	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		r, err := p.Ping(pctx, target, time.Second)
		cancel()
		if err == nil && r.OK {
			out = append(out, r.RTT)
		}
		if i < count-1 {
			sleep(ctx, interval)
		}
	}
	return out
}

func samplePingsDuration(ctx context.Context, p probe.Pinger, target net.IP, dur, interval time.Duration) []time.Duration {
	var out []time.Duration
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		r, err := p.Ping(pctx, target, time.Second)
		cancel()
		if err == nil && r.OK {
			out = append(out, r.RTT)
		}
		// Skip the trailing sleep if the next probe would fall past the deadline.
		if time.Now().Add(interval).Before(deadline) {
			sleep(ctx, interval)
		}
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}
