// Package bufferbloat measures latency-under-load: the increase in RTT while the
// link is saturated. This is the single most under-reported metric for gamers
// ("my ping is fine until someone streams Netflix"). It establishes an idle
// baseline, saturates the downlink with parallel HTTP transfers (see
// internal/loadgen), samples RTT during the load, and grades the delta on the
// DSLReports A+…F scale. The speed test (internal/speedtest) covers the upload
// direction as well.
package bufferbloat

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/loadgen"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// minLoadBytes is the least the load phase must move for a grade to mean
// anything; below it the link was never saturated and Run reports failure.
const minLoadBytes = 2 * 1024 * 1024

// Options configures a bufferbloat run.
type Options struct {
	// Sources are candidate load endpoints; the first that serves us is used
	// to saturate the link. Defaults to loadgen.DefaultSources.
	Sources []loadgen.Source
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
	// Progress, when set, receives every sampled RTT with its phase ("idle" or
	// "load") so a UI can show live numbers. Lost probes are reported as 0.
	Progress func(phase string, rtt time.Duration)
}

// DefaultOptions returns sensible defaults (~10s total test).
func DefaultOptions() Options {
	return Options{
		Sources:         loadgen.DefaultSources,
		Connections:     6,
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
	Source       string        // load endpoint used (e.g. "Cloudflare")
	Colo         string        // CDN point of presence that served the load, if known
	IdleSamples  int
	LoadSamples  int
}

// Run executes a download-saturation bufferbloat test. It is safe to cancel via
// ctx.
func Run(ctx context.Context, p probe.Pinger, opt Options) (Result, error) {
	o := DefaultOptions()
	if len(opt.Sources) > 0 {
		o.Sources = opt.Sources
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
	o.Progress = opt.Progress

	var res Result

	// Phase 1: idle baseline.
	idle := samplePings(ctx, p, o.PingTarget, o.BaselineSamples, o.PingInterval, o.report("idle"))
	res.IdleSamples = len(idle)
	if len(idle) == 0 {
		return res, fmt.Errorf("bufferbloat: no idle baseline samples (target unreachable?)")
	}
	res.IdleRTT = median(idle)

	// Pick a load endpoint that actually serves us; fail clearly rather than
	// reporting a bogus grade with no load.
	remaining := append([]loadgen.Source(nil), o.Sources...)
retry:
	src, colo, err := loadgen.PickSource(ctx, remaining)
	if err != nil {
		return res, fmt.Errorf("bufferbloat: %w", err)
	}
	res.Source, res.Colo = src.Name, colo

	// Phase 2: saturate + sample under load.
	loadCtx, cancelLoad := context.WithCancel(ctx)
	var bytesRead atomic.Uint64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		loadgen.Download(loadCtx, src, o.Connections, &bytesRead)
	}()

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
	loaded := samplePingsDuration(loadCtx, p, o.PingTarget, o.LoadDuration, o.PingInterval, o.report("load"))
	elapsed := time.Since(startTime)
	endBytes := bytesRead.Load()

	cancelLoad()
	wg.Wait()

	res.LoadSamples = len(loaded)
	res.DownloadMbps = loadgen.Mbps(endBytes-startBytes, elapsed)
	// If essentially nothing downloaded, the link was never saturated, so any
	// grade would be meaningless — report the failure instead.
	if endBytes-startBytes < minLoadBytes {
		// The source answered the probe but throttled the streams: try the next one.
		remaining = loadgen.Without(remaining, src.Name)
		if len(remaining) > 0 && ctx.Err() == nil {
			goto retry
		}
		return res, fmt.Errorf("bufferbloat: link was not saturated (only %d bytes downloaded from %s); result is not meaningful", endBytes-startBytes, src.Name)
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

// report adapts the optional Progress callback for one phase.
func (o Options) report(phase string) func(time.Duration) {
	if o.Progress == nil {
		return nil
	}
	return func(rtt time.Duration) { o.Progress(phase, rtt) }
}

func samplePings(ctx context.Context, p probe.Pinger, target net.IP, count int, interval time.Duration, report func(time.Duration)) []time.Duration {
	var out []time.Duration
	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		r, err := p.Ping(pctx, target, time.Second)
		cancel()
		var rtt time.Duration
		if err == nil && r.OK {
			rtt = r.RTT
			out = append(out, r.RTT)
		}
		if report != nil {
			report(rtt)
		}
		if i < count-1 {
			sleep(ctx, interval)
		}
	}
	return out
}

func samplePingsDuration(ctx context.Context, p probe.Pinger, target net.IP, dur, interval time.Duration, report func(time.Duration)) []time.Duration {
	var out []time.Duration
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		r, err := p.Ping(pctx, target, time.Second)
		cancel()
		var rtt time.Duration
		if err == nil && r.OK {
			rtt = r.RTT
			out = append(out, r.RTT)
		}
		if report != nil {
			report(rtt)
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
